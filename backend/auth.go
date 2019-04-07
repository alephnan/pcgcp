package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/dgrijalva/jwt-go"
	xsrf "golang.org/x/net/xsrftoken"
)

var (
	CONTAINER_JWT_KEY                 = []byte("my_secret_key_2")
	JWT_KEY                           = []byte("my_secret_key")
	SESSION_EXPIRATION_MINUTES        = 5
	SESSION_REFRESH_THRESHOLD_MINUTES = 1
	XSRF_KEY                          = "my_secret_key"
	XSRF_ACTION_ID                    = "global"
)

func auth_Login(w http.ResponseWriter, r *http.Request) {
	auth_sign(w)
}

func auth_Refresh(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth_verify(w, r)
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

	auth_sign(w)
}

func auth_AuthTest(w http.ResponseWriter, r *http.Request) {
	claims, containedJwt := auth_verify(w, r)
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

func auth_signWithClaims(w http.ResponseWriter, key []byte, claims jwt.Claims) *string {
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

func auth_sign(w http.ResponseWriter) {
	expirationTime := time.Now().Add(time.Duration(SESSION_EXPIRATION_MINUTES) * time.Minute)
	// In JWT, the expiry time is expressed as unix milliseconds
	standardClaims := jwt.StandardClaims{ExpiresAt: expirationTime.Unix()}

	// Sign inner JWT
	containedJwt := auth_signWithClaims(w, JWT_KEY, &Claims{
		Username:       "foo",
		StandardClaims: standardClaims,
	})
	if containedJwt == nil {
		return
	}

	// Sign outer JWT.
	containerJwt := auth_signWithClaims(w, CONTAINER_JWT_KEY, &ContainerClaims{
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

func auth_extractClaims(w http.ResponseWriter, jwtStr string, key []byte, claims jwt.Claims) bool {
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

func auth_verify(w http.ResponseWriter, r *http.Request) (*Claims, *string) {
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
	if success := auth_extractClaims(w, c.Value, CONTAINER_JWT_KEY, containerClaims); !success {
		return nil, nil
	}
	containedJwt := containerClaims.ContainedJwt

	claims := &Claims{}
	if success := auth_extractClaims(w, containedJwt, JWT_KEY, claims); !success {
		return nil, nil
	}

	return claims, &containedJwt
}
