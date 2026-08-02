package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	proxydebugger "cursor/cursor-proxy-debugger"

	"github.com/pkg/browser"
)

func main() {
	config := proxydebugger.Config{}
	openBrowser := true
	flag.StringVar(&config.ProxyAddr, "proxy-addr", "127.0.0.1:9090", "HTTP/HTTPS 代理监听地址")
	flag.StringVar(&config.UIAddr, "ui-addr", "127.0.0.1:9091", "调试界面监听地址")
	flag.StringVar(&config.TargetHost, "target-host", "api2.cursor.sh", "需要解密和抓取的目标主机")
	flag.IntVar(&config.MaxExchanges, "max-exchanges", 200, "内存中保留的最大请求数")
	flag.BoolVar(&openBrowser, "open", true, "启动后打开浏览器")
	flag.Parse()

	server, err := proxydebugger.New(config)
	if err != nil {
		log.Fatal(err)
	}
	if err := server.Start(); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Cursor 协议调试代理已启动\n")
	fmt.Printf("代理地址: http://%s\n", server.ProxyAddr())
	fmt.Printf("调试界面: %s\n", server.UIURL())
	if openBrowser {
		_ = browser.OpenURL(server.UIURL())
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals

	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(shutdownContext); err != nil {
		log.Printf("关闭调试代理失败：%v", err)
	}
}
