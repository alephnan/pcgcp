package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	verifier "github.com/alephnan/google-auth-id-token-verifier"
	"github.com/dgrijalva/jwt-go"
	"github.com/namsral/flag"
	"github.com/tjarratt/babble"
	xsrf "golang.org/x/net/xsrftoken"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	crm "google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/option"
)

type TemplateModel_Index struct {
	BuildName string
	BuildTime string
	ClientId  string
}

type ContainerClaims struct {
	ContainedJwt string `json:"jwt"`
	jwt.StandardClaims
}

type Claims struct {
	Username string `json:"username"`
	jwt.StandardClaims
}

type AuthorizationStruct struct {
	Code     string
	Id_Token string
}

type HealthResponse struct {
}

// TODO: populate more fields
// TODO: Support orgs and folders.
type AuthorizationResponse struct {
	Projects []string `json:"projects"`
}

var (
	buildName   = babble.NewBabbler().Babble()
	buildTime   = time.Now().Format(time.Stamp)
	defaultPort = 8080

	logger     = log.New(os.Stdout, "[cetea] ", 0)
	colorGreen = string([]byte{27, 91, 57, 55, 59, 51, 50, 59, 49, 109})
	colorReset = string([]byte{27, 91, 48, 109})

	googleAuth            *oauth2.Config
	googleIdTokenVerifier = verifier.Verifier{}
	idTokenAudience       []string

	HEALTH_RESPONSE = HealthResponse{}

	users = map[string]string{
		"user1": "password1",
		"user2": "password2",
	}

	CONTAINER_JWT_KEY                 = []byte("my_secret_key_2")
	JWT_KEY                           = []byte("my_secret_key")
	SESSION_EXPIRATION_MINUTES        = 5
	SESSION_REFRESH_THRESHOLD_MINUTES = 1
	XSRF_KEY                          = "my_secret_key"
	XSRF_ACTION_ID                    = "global"
)

func main() {
	logger.Printf("Build: %s %s %s - %s \n", colorGreen, buildName, colorReset, buildTime)

	port := flag.Int("port", defaultPort, "port to listen on")
	dev := flag.Bool("dev", false, "port to listen on")
	flag.Parse()

	file, err := ioutil.ReadFile("./config/client_secret.json")
	if err != nil {
		// TODO: Signal bash script and/or Docker host and get them to terminate.
		panic(err)
	}
	googleAuth, err = google.ConfigFromJSON(file)
	if err != nil {
		panic(err)
	}
	idTokenAudience = []string{googleAuth.ClientID}

	server := startServerInBackground(*port, *dev)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	sig := <-stop
	logger.Println()
	logger.Println("Received signal", sig.String())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := server.Shutdown(ctx); err != nil {
		cancel()
		logger.Println("Error shutting down.")
	} else {
		logger.Println("Graceful shutdown.")
	}
	logger.Println("Exiting.")
}

func startServerInBackground(port int, dev bool) *http.Server {
	logger.Printf("Running on port: %s %s %s ", colorGreen, strconv.Itoa(port), colorReset)
	addr := ":" + strconv.Itoa(port)
	srv := &http.Server{Addr: addr}

	if dev {
		logger.Printf("Serving static files")
		fs := http.FileServer(http.Dir("static"))
		http.Handle("/", http.StripPrefix("", fs))
	}

	http.HandleFunc("/api/health", health)
	http.HandleFunc("/api/authorization", authorization)
	http.HandleFunc("/api/auth/login", login)
	http.HandleFunc("/api/auth/refresh", refresh)
	http.HandleFunc("/api/auth/test", authTest)
	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			logger.Panic(err)
			panic(err)
		}
	}()
	return srv
}

func signWithClaims(w http.ResponseWriter, key []byte, claims jwt.Claims) *string {
	// Declare the token with the algorithm used for signing, and the claims
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	// Create the JWT string
	jwtStr, err := token.SignedString(key)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return nil
	}
	return &jwtStr
}

func sign(w http.ResponseWriter) {
	expirationTime := time.Now().Add(time.Duration(SESSION_EXPIRATION_MINUTES) * time.Minute)
	// In JWT, the expiry time is expressed as unix milliseconds
	standardClaims := jwt.StandardClaims{ExpiresAt: expirationTime.Unix()}

	// Sign inner JWT
	containedJwt := signWithClaims(w, JWT_KEY, &Claims{
		Username:       "foo",
		StandardClaims: standardClaims,
	})
	if containedJwt == nil {
		return
	}

	// Sign outer JWT.
	containerJwt := signWithClaims(w, CONTAINER_JWT_KEY, &ContainerClaims{
		ContainedJwt:   *containedJwt,
		StandardClaims: standardClaims,
	})
	if containerJwt == nil {
		return
	}

	// Set the client cookie for "token" as the container JWT we just generated
	// we also set an expiry time which is the same as the token itself.
	http.SetCookie(w, &http.Cookie{
		Name:    "token",
		Value:   *containerJwt,
		Expires: expirationTime,
		// prevents cookie from being read by JavaScript. Cookie will still
		// be automatically attached to http requests. This has
		// nothing to do with https vs http
		HttpOnly: true,
	})
	// By generating the XSRF token using the JWT, the xsrf token is valid
	// only if the JWT is valid, sidestepping limitation of net/xsrftoken library
	// having 24 hour expiration, and pose risk where if the XSRF token cookie
	// is leaked or stolen, it can only be used with the corresponding JWT and
	// none other.
	xsrfToken := xsrf.Generate(XSRF_KEY, *containedJwt, XSRF_ACTION_ID)
	// Since some time has elapsed after the time xsrfToken issued, we want the
	// cookie to expire shortly before the token does. This doesn't matter too
	// much as the xsrf-token lifespan bounded by JWT's lifespan, as long as JWT
	// is verified first, and expiration shortcircuits request.
	xsrfCookieExpiration := time.Now().Add(xsrf.Timeout).Add(time.Duration(-1 * time.Minute))
	http.SetCookie(w, &http.Cookie{
		Name:    "XSRF-TOKEN",
		Value:   xsrfToken,
		Expires: xsrfCookieExpiration,
	})
}

func login(w http.ResponseWriter, r *http.Request) {
	sign(w)
}

func extractClaims(w http.ResponseWriter, jwtStr string, key []byte, claims jwt.Claims) bool {
	// Parse the JWT string and store the result in `claims`.
	// Note that we are passing the key in this method as well. This method will return an error
	// if the token is invalid (if it has expired according to the expiry time we set on sign in),
	// or if the signature does not match
	token, err := jwt.ParseWithClaims(jwtStr, claims, func(token *jwt.Token) (interface{}, error) {
		return key, nil
	})
	if !token.Valid {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	if err != nil {
		if err == jwt.ErrSignatureInvalid {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		w.WriteHeader(http.StatusBadRequest)
		return false
	}

	return true
}

func verify(w http.ResponseWriter, r *http.Request) (*Claims, *string) {
	// Extract the session cookie.
	c, err := r.Cookie("token")
	if err != nil {
		if err == http.ErrNoCookie {
			w.WriteHeader(http.StatusUnauthorized)
			return nil, nil
		}
		w.WriteHeader(http.StatusBadRequest)
		return nil, nil
	}

	containerClaims := &ContainerClaims{}
	if success := extractClaims(w, c.Value, CONTAINER_JWT_KEY, containerClaims); !success {
		return nil, nil
	}
	containedJwt := containerClaims.ContainedJwt

	claims := &Claims{}
	if success := extractClaims(w, containedJwt, JWT_KEY, claims); !success {
		return nil, nil
	}

	return claims, &containedJwt
}

func refresh(w http.ResponseWriter, r *http.Request) {
	claims, _ := verify(w, r)
	if claims == nil {
		return
	}

	// We ensure that a new token is not issued until enough time has elapsed
	// In this case, a new token will only be issued if the old token is within
	// expiry threshold. Otherwise, return a bad request status
	if time.Unix(claims.ExpiresAt, 0).Sub(time.Now()) > time.Duration(SESSION_REFRESH_THRESHOLD_MINUTES)*time.Minute {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	sign(w)
}

func authTest(w http.ResponseWriter, r *http.Request) {
	claims, containedJwt := verify(w, r)
	if claims == nil {
		return
	}
	xXsrfTokenHeader := r.Header.Get("X-XSRF-TOKEN")
	if xXsrfTokenHeader == "" {
		http.Error(w, "Missing XSRF", http.StatusForbidden)
		return
	}

	isValidXsrf := xsrf.Valid(xXsrfTokenHeader, XSRF_KEY, *containedJwt, XSRF_ACTION_ID)
	if !isValidXsrf {
		http.Error(w, "Invalid XSRF", http.StatusForbidden)
		return
	}

	w.Write([]byte(fmt.Sprintf("Welcome %s!", claims.Username)))
}

func health(w http.ResponseWriter, r *http.Request) {
	// TODO: this should be refactored into middleware / interceptor
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(HEALTH_RESPONSE)
}

func authorization(w http.ResponseWriter, r *http.Request) {
	// https://stackoverflow.com/questions/17478731/whats-the-point-of-the-x-requested-with-header
	if xRequestedWithHeader := r.Header.Get("X-Requested-With"); xRequestedWithHeader != "XMLHttpRequest" {
		http.Error(w, "Untrusted request", http.StatusForbidden)
		return
	}
	if r.Body == nil {
		http.Error(w, "Please send a request body", 400)
		return
	}
	var auth AuthorizationStruct
	err := json.NewDecoder(r.Body).Decode(&auth)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	_, err = verifyIdToken(auth.Id_Token)
	if err != nil {
		http.Error(w, "Cannot verify id_token JWT", http.StatusForbidden)
		return
	}

	token, err := googleAuth.Exchange(oauth2.NoContext, auth.Code)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if token == nil {
		http.Error(w, "No token response received", http.StatusForbidden)
	}

	ctx := context.Background()
	crmService, err := crm.NewService(ctx, option.WithTokenSource(googleAuth.TokenSource(ctx, token)))
	projectsResponse, err := crmService.Projects.List().Do()
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	projects := projectsResponse.Projects
	// TODO: handle non 200 HTTP responses?
	// TODO: handle empty project list
	var projectNames = make([]string, len(projects))
	for i := 0; i < len(projects); i++ {
		projectNames[i] = projects[i].Name
	}
	responseStruct := AuthorizationResponse{Projects: projectNames}
	response, err := json.Marshal(responseStruct)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	io.WriteString(w, string(response))
}

func verifyIdToken(idToken string) (*verifier.ClaimSet, error) {
	logger.Printf("Verifying id_token: " + idToken)
	err := googleIdTokenVerifier.VerifyIDToken(idToken, idTokenAudience)
	if err != nil {
		logger.Printf("Error verifying id_token.")
		return nil, err
	}
	claims, err := verifier.Decode(idToken)
	if err != nil {
		logger.Print("Error decoding id_token.")
	}
	return claims, err
}
