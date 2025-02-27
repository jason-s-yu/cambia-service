package handlers

import "strings"

// extractCookieToken extracts a named cookie value from "Cookie" header, or returns empty if not found.
func extractCookieToken(cookieHeader, cookieName string) string {
	parts := strings.Split(cookieHeader, cookieName+"=")
	if len(parts) < 2 {
		return ""
	}
	token := parts[1]
	if idx := strings.Index(token, ";"); idx != -1 {
		token = token[:idx]
	}
	return token
}
