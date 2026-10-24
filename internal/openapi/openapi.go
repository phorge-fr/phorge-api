// Package openapi serves the OpenAPI document of the API and a Swagger UI that reads it.
//
// The document is written by hand (openapi.yaml) and a test compares it with the routes that are really registered, so it
// cannot drift: a route without a description, or a description without a route, fails the build.
package openapi

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var spec []byte

// Spec is the OpenAPI document.
func Spec() []byte { return spec }

// SpecHandler serves the OpenAPI document.
func SpecHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(spec)
	})
}

// swaggerUIVersion and the two hashes below go together: the browser refuses the script or the stylesheet if the CDN
// ever serves something else (Subresource Integrity). To update, change the version and recompute both with
//
//	curl -sL https://cdn.jsdelivr.net/npm/swagger-ui-dist@<version>/<file> | openssl dgst -sha384 -binary | openssl base64 -A
const (
	swaggerUIVersion = "5.34.0"
	swaggerUICSSHash = "sha384-4c3vsE5f+trhFyHrFrLM1fFGUn531NIaC7dcqEWIJdoRQJ5CkJbSuSCwXLpaS1KF"
	swaggerUIJSHash  = "sha384-9Td7MKiIr2S83E1kZyyVi8KdJw/S8CpAvu0llLyMeg0xq/hDGiZJfVn0fV4llCmK"
)

const page = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Phorge API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui.css" integrity="` + swaggerUICSSHash + `" crossorigin="anonymous">
</head>
<body>
<div id="swagger-ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui-bundle.js" integrity="` + swaggerUIJSHash + `" crossorigin="anonymous"></script>
<script src="docs/init.js"></script>
</body>
</html>
`

// The initialisation is a file of its own, not an inline script, so that the page can forbid inline scripts.
const initScript = `window.ui = SwaggerUIBundle({
  url: "openapi.yaml",
  dom_id: "#swagger-ui",
  deepLinking: true,
  persistAuthorization: false,
});
`

const csp = "default-src 'none'; " +
	"script-src 'self' https://cdn.jsdelivr.net; " +
	"style-src 'self' https://cdn.jsdelivr.net 'unsafe-inline'; " + // Swagger UI sets styles from script
	"img-src 'self' data: https://cdn.jsdelivr.net; " +
	"font-src https://cdn.jsdelivr.net data:; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// UIHandler serves the Swagger UI page.
func UIHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", csp)
		_, _ = w.Write([]byte(page))
	})
}

// InitScriptHandler serves the script that starts Swagger UI.
func InitScriptHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write([]byte(initScript))
	})
}
