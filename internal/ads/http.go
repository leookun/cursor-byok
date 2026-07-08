package ads

import (
	"net/http"
	"strings"
)

const bridgeScript = `<script>
(function () {
  var source = "cursor-ad";
  function post(type, payload) {
    if (!window.parent) {
      return;
    }
    var message = Object.assign({ source: source, type: type }, payload || {});
    window.parent.postMessage(message, "*");
  }
  window.AdBridge = Object.assign({}, window.AdBridge || {}, {
    close: function () {
      post("close");
    },
    openExternal: function (url) {
      post("openExternal", { url: String(url || "") });
    }
  });
})();
</script>`

func NewHTTPHandler(storeRoot string) http.Handler {
	service := NewService(Options{StoreRoot: storeRoot})
	return http.HandlerFunc(service.ServeHTTP)
}

func (service *Service) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request == nil {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	asset, packageHash, ok, err := service.LoadAsset(request.Context(), request.URL.Path)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	if !ok {
		http.NotFound(writer, request)
		return
	}
	payload := asset.data
	contentType := strings.TrimSpace(asset.contentType)
	if asset.path == "index.html" {
		payload = injectBridge(payload)
		contentType = "text/html; charset=utf-8"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if packageHash != "" {
		writer.Header().Set("ETag", `"`+packageHash+`"`)
	}
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(payload)
}

func injectBridge(data []byte) []byte {
	html := string(data)
	lower := strings.ToLower(html)
	if index := strings.Index(lower, "</head>"); index >= 0 {
		return []byte(html[:index] + bridgeScript + html[index:])
	}
	if index := strings.Index(lower, "<body"); index >= 0 {
		return []byte(html[:index] + bridgeScript + html[index:])
	}
	return []byte(bridgeScript + html)
}
