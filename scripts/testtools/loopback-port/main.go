package main

import (
	"fmt"
	"net"
	"os"
)

func main() {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "无法临时绑定 loopback 端口")
		os.Exit(1)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.Port < 1 || address.Port > 65535 {
		listener.Close()
		fmt.Fprintln(os.Stderr, "临时 loopback 端口无效")
		os.Exit(1)
	}
	fmt.Println(address.Port)
	if err := listener.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "释放临时 loopback 端口失败")
		os.Exit(1)
	}
}
