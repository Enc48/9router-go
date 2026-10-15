package executor

import (
	"net/http"

	"9router/proxy/internal/proxy"
)

// ForwardGeminiWeb refuses a chat request on gemini-web with the reason it
// cannot be served.
//
// gemini.google.com has no API the web client calls directly: the site drives
// its own backend from inside the page, and the answer only exists in the
// XHR response the page receives. OmniRoute reaches it by launching a real
// Chromium, injecting the pasted __Secure-1PSID cookies, typing the prompt into
// the composer and intercepting the first StreamGenerate response
// (open-sse/executors/gemini-web.ts). There is no HTTP surface to call.
//
// This build is a single CGO-free binary, so it ships no browser and cannot
// acquire one at runtime without breaking that guarantee. Rather than forward
// the request to gemini.google.com and return a 401 that reads as an expired
// cookie, the entry is refused with the reason. The provider stays in the
// catalog so its models are listable and its credentials validatable, which is
// what a user needs before deciding how to run it.
func ForwardGeminiWeb(w http.ResponseWriter, _ *Request) error {
	return proxy.UpstreamFailure(http.StatusNotImplemented,
		"gemini-web is not routable in this build: gemini.google.com only answers from inside its own web client, "+
			"which upstream drives with an embedded Chromium. 9router-go ships no browser. "+
			"Use the gemini API-key provider for Gemini, or run a build that embeds a browser.")
}
