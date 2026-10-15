package plex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeTV(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/pins", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("strong") != "true" || r.Header.Get("X-Plex-Client-Identifier") != "cid" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"id":42,"code":"abc","authToken":null}`))
	})
	mux.HandleFunc("GET /api/v2/pins/42", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":42,"code":"abc","authToken":"tok"}`))
	})
	mux.HandleFunc("GET /api/v2/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"id":7,"uuid":"u","username":"neo","title":"neo","email":"n@x","thumb":"https://plex.tv/t.png"}`))
	})
	mux.HandleFunc("GET /api/v2/resources", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
		 {"name":"NAS","product":"Plex Media Server","clientIdentifier":"srv1","provides":"server","owned":true,"accessToken":"at1",
		  "connections":[{"protocol":"http","address":"192.168.1.2","port":32400,"uri":"http://192.168.1.2:32400","local":true,"relay":false}]},
		 {"name":"iPhone","product":"Plex for iOS","clientIdentifier":"ph","provides":"client,player","owned":true},
		 {"name":"Friend","product":"Plex Media Server","clientIdentifier":"srv2","provides":"server,player","owned":false,"accessToken":"at2"}
		]`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	c := New("cid", "test")
	c.TVBase, c.ClientsBase = ts.URL, ts.URL
	return c
}

func TestSignInFlow(t *testing.T) {
	c, ctx := fakeTV(t), context.Background()
	pin, err := c.CreatePin(ctx)
	if err != nil || pin.ID != 42 || pin.AuthToken != "" {
		t.Fatalf("create: %+v %v", pin, err)
	}
	u := c.AuthURL(pin.Code, "https://a.example/auth/plex/return")
	if !strings.HasPrefix(u, "https://app.plex.tv/auth#?") || !strings.Contains(u, "code=abc") || !strings.Contains(u, "forwardUrl=") {
		t.Fatalf("auth url %s", u)
	}
	pin, err = c.CheckPin(ctx, 42)
	if err != nil || pin.AuthToken != "tok" {
		t.Fatalf("check: %+v %v", pin, err)
	}
	acc, err := c.Account(ctx, "tok")
	if err != nil || acc.ID != 7 || acc.DisplayName() != "neo" {
		t.Fatalf("account: %+v %v", acc, err)
	}
	if _, err := c.Account(ctx, "bad"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token: %v", err)
	}
	srv, err := c.Servers(ctx, "tok")
	if err != nil || len(srv) != 2 || srv[0].ClientIdentifier != "srv1" || !srv[0].Owned || srv[1].Owned {
		t.Fatalf("servers: %+v %v", srv, err)
	}
}
