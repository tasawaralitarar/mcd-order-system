package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// グローバル変数としてログとDBインスタンスを保持
var logger *log.Logger

func main() {
	// 1. ログの初期化
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Fatalf("ログディレクトリの作成に失敗しました: %v", err)
	}

	logFile, err := os.OpenFile(filepath.Join(logDir, "order.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("ログファイルのオープンに失敗しました: %v", err)
	}
	defer logFile.Close()

	// 標準出力とファイル出力の両方にマルチ出力
	multiWriter := io.MultiWriter(os.Stdout, logFile)
	logger = log.New(multiWriter, "", log.LstdFlags)

	logger.Println("[INFO] --- アプリケーション起動シーケンス開始 ---")

	// 2. データベースの初期化
	initDB()
	defer db.Close()

	// 3. ルーター（マルチプレクサ）設定 (Go 1.22+ 標準ルーティング機能)
	mux := http.NewServeMux()

	// 注文管理機能
	mux.HandleFunc("POST /api/orders", handleCreateOrder)
	mux.HandleFunc("GET /api/orders", handleListOrders)
	mux.HandleFunc("GET /api/orders/{orderNo}", handleGetOrder)
	mux.HandleFunc("PUT /api/orders/{orderNo}/status", handleUpdateOrderStatus)

	// フロント掲示板機能
	mux.HandleFunc("POST /api/board", handleBoardAction)

	// 厨房機能
	mux.HandleFunc("POST /api/kitchen", handleKitchenAction)

	// CORSミドルウェアを適用
	handlerWithCORS := corsMiddleware(mux)

	// 4. HTTP サーバー設定
	serverAddr := "0.0.0.0:8080"
	server := &http.Server{
		Addr:    serverAddr,
		Handler: handlerWithCORS,
	}

	// 5. グレースフルシャットダウンの実装
	idleConnsClosed := make(chan struct{})
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		sig := <-sigChan
		logger.Printf("[INFO] シグナルを受信しました (%v)。シャットダウンを開始します...", sig)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			logger.Printf("[ERROR] サーバーのグレースフルシャットダウン中にエラーが発生しました: %v", err)
		}
		close(idleConnsClosed)
	}()

	logger.Printf("[INFO] サーバーがポート 8080 で起動しました (%s)...", serverAddr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Fatalf("[FATAL] サーバーの起動に失敗しました: %v", err)
	}

	<-idleConnsClosed
	logger.Println("[INFO] --- アプリケーションが正常に終了しました ---")
}