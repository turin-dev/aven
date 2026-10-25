package api

import (
	_ "embed"
	"net/http"
)

//go:embed graphiql.html
var graphiqlHTML []byte

func (s *Server) graphiql(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(graphiqlHTML)
}
