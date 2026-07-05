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
	// 广告系统已彻底关闭
	http.NotFound(writer, request)
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
