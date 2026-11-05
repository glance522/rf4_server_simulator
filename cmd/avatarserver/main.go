package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	addr := flag.String("addr", ":8090", "HTTP 服务监听地址")
	avatarDir := flag.String("dir", "avatars", "头像文件目录")
	flag.Parse()

	info, err := os.Stat(*avatarDir)
	if err != nil {
		log.Fatalf("读取头像目录 %q: %v", *avatarDir, err)
	}
	if !info.IsDir() {
		log.Fatalf("头像路径 %q 不是目录", *avatarDir)
	}

	mux := http.NewServeMux()
	mux.Handle("/avatars/", http.StripPrefix(
		"/avatars/",
		http.FileServer(http.Dir(*avatarDir)),
	))

	log.Printf("头像服务监听 %s，目录 %s", *addr, *avatarDir)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(fmt.Errorf("头像服务退出: %w", err))
	}
}
