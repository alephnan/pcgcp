import Vue from "vue";
import Vuex from "vuex";
import {AuthState} from "./enum";

Vue.use(Vuex);

export default new Vuex.Store({
  state: {
    auth: {
      state: AuthState.LoggedOut,
      email: null,
    },
    sidebar: false
  },
  mutations: {
    auth(state, payload) {
      if(!payload.email) {
        if(payload.state == AuthState.Verifying || payload.state == AuthState.Verified) {
          // TODO: throw invalid state transition error.
        }
      }
      state.auth = payload;
    },
    showSidebar(state) {
      (document as any).getElementById('sidenav').classList.remove("hidden");
      state.sidebar = true;
    }
  },
  actions: {
    signin: ({commit, dispatch}, payload) => {
      commit("auth", {state: AuthState.LoggingIn});
      // https://developers.google.com/identity/sign-in/web/reference#gapiauth2offlineaccessoptions
      // const prompt = "select_account";
      // const prompt = "consent;"
      (window as any).auth2.grantOfflineAccess().then((response: any) => {
        // Change state
        if(!response.code) {
          commit("auth", {state: AuthState.Error});
          console.log("Error.");
          return;
        }
        const googleUser = (window as any).auth2.currentUser.get();
        const profile = googleUser.getBasicProfile();
        const email = profile.getEmail();
        // BasicProfile.getId()
        // BasicProfile.getName()
        // BasicProfile.getGivenName()
        // BasicProfile.getFamilyName()
        // BasicProfile.getImageUrl()
        // BasicProfile.getEmail();
        commit("auth", {
          state: AuthState.Verifying,
          email
        });
        commit("showSidebar");
        dispatch("verify", response);
      });
    },
    verify: ({commit, dispatch, state}, payload) => {
      console.log("Verifying with backend.");

      const response = payload;
      // https://developers.google.com/identity/sign-in/web/reference#googleusergetid
      const googleUser = (window as any).auth2.currentUser.get();
      const {id_token} = googleUser.getAuthResponse();
      fetch("http://localhost:8080/api/authorization", {
          method: "POST",
          cache: "no-cache",
          credentials: "same-origin",
          headers: {
            "Content-Type": "application/json",
            "X-Requested-With": "XMLHttpRequest"
          },
          body: JSON.stringify({
            "code": response.code,
            // TODO: verify id_token on server
            "id_token": id_token
          })
      }).then(response => {
        // TODO: Handle error response
        commit("auth", {
          state: AuthState.Verified,
          email: state.auth.email
        });
        dispatch('handleVerificationResponse', response);
      });
    },
    handleVerificationResponse: ({commit}, payload) => {
      payload.json().then((json: any) => {
        const projectNames = json.projects;
        const newUl = document.createElement('ul');
        newUl.id = "sidenav-projectlist";
        for(let i = 0 ; i < projectNames.length; i++) {
          const item = document.createElement('li');
          const a = document.createElement('a');
          a.setAttribute("href", "")
          a.appendChild(document.createTextNode(projectNames[i]));
          item.appendChild(a);
          // https://coderwall.com/p/o9ws2g/why-you-should-always-append-dom-elements-using-documentfragments
          newUl.appendChild(item);
        }
        const frag = document.createDocumentFragment();
        frag.appendChild(newUl);
        const ul: any = document.getElementById("sidenav-projectlist");
        ul.parentNode.replaceChild(frag, ul as any);

        (document as any).getElementById('sidenav-projectlist-spinner-container').classList.add("hidden");
        (document as any).getElementById('sidenav-projectlist-container').classList.remove("hidden");
      });
    }
  }
});
