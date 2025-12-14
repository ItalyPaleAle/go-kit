package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

func RespondWithJSON(w http.ResponseWriter, r *http.Request, data any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	err := enc.Encode(data)
	if err != nil {
		slog.WarnContext(r.Context(), "Error writing JSON response", slog.Any("error", err))
	}
}
