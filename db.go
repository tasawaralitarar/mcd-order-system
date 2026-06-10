package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB

const (
	StatusReceived  = "オーダ受信済み"
	StatusCooking   = "調理済み"
	StatusDelivered = "受け渡し済み"
)

// データベースの初期化と接続設定
func initDB() {
	var err error
	// 同時書き込み競合対策のタイムアウト設定を追加
	dsn := "order.db?_busy_timeout=5000"
	db, err = sql.Open("sqlite3", dsn)
	if err != nil {
		log.Fatalf("[FATAL] データベース接続エラー: %v", err)
	}

	// SQLiteの並行書き込み制約によるロック回避のため、最大オープン接続数を1に制限
	db.SetMaxOpenConns(1)

	// テーブルの自動作成
	schema := `
	CREATE TABLE IF NOT EXISTS order_items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		order_no TEXT NOT NULL,
		terminal_no TEXT NOT NULL,
		order_status TEXT NOT NULL,
		item_no INTEGER NOT NULL,
		menu_name TEXT NOT NULL,
		unit_price INTEGER NOT NULL,
		quantity INTEGER NOT NULL,
		subtotal INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_order_no ON order_items(order_no);
	CREATE INDEX IF NOT EXISTS idx_order_status ON order_items(order_status);
	`
	if _, err := db.Exec(schema); err != nil {
		log.Fatalf("[FATAL] テーブル作成エラー: %v", err)
	}
	log.Println("[INFO] データベースが正常に初期化されました (order.db)")
}

// 注文明細を表す内部構造体
type OrderItemRow struct {
	ID           int64
	OrderNo      string
	TerminalNo   string
	OrderStatus  string
	ItemNo       int
	MenuName     string
	UnitPrice    int
	Quantity     int
	Subtotal     int
	CreatedAt    time.Time
}

// 同一トランザクション内で採番と保存をアトミックに実行する
func saveOrderTx(terminalNo string, items []OrderItemInput) (string, error) {
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback() // エラー時は自動ロールバック

	// 1. 本日の日付(MMDD)を取得
	now := time.Now()
	dateStr := now.Format("0102") // MMDD形式

	// 2. 本日の最新の連番をトランザクション内で検索してロック同等の確定を行う
	// SQLiteではMaxOpenConns(1)とTxにより直列化されるため、重複が防がれます
	var lastOrderNo sql.NullString
	query := `SELECT order_no FROM order_items WHERE order_no LIKE ? ORDER BY id DESC LIMIT 1`
	err = tx.QueryRow(query, dateStr+"-%").Scan(&lastOrderNo)

	nextSeq := 1
	if err == nil && lastOrderNo.Valid && len(lastOrderNo.String) == 8 {
		// 既存の "MMDD-NNN" から末尾3桁の数値をパース
		var seq int
		_, err := fmt.Sscanf(lastOrderNo.String[5:], "%d", &seq)
		if err == nil {
			nextSeq = seq + 1
		}
	} else if err != nil && err != sql.ErrNoRows {
		return "", err
	}

	// 3. 注文番号のフォーマット
	orderNo := fmt.Sprintf("%s-%03d", dateStr, nextSeq)

	// 4. 明細データの挿入
	insertQuery := `
	INSERT INTO order_items (order_no, terminal_no, order_status, item_no, menu_name, unit_price, quantity, subtotal)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	for i, item := range items {
		itemNo := i + 1
		subtotal := item.UnitPrice * item.Quantity
		_, err := tx.Exec(insertQuery, orderNo, terminalNo, StatusReceived, itemNo, item.MenuName, item.UnitPrice, item.Quantity, subtotal)
		if err != nil {
			return "", err
		}
	}

	// 5. コミットして確定
	if err := tx.Commit(); err != nil {
		return "", err
	}

	return orderNo, nil
}

// 特定のステータスに合致する注文番号の一覧を取得
func getOrderNosByStatus(status string) ([]string, error) {
	query := `SELECT DISTINCT order_no FROM order_items WHERE order_status = ? ORDER BY id ASC`
	rows, err := db.Query(query, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orderNos []string
	for rows.Next() {
		var oNo string
		if err := rows.Scan(&oNo); err != nil {
			return nil, err
		}
		orderNos = append(orderNos, oNo)
	}
	return orderNos, nil
}

// ステータスを更新する汎用関数
func updateStatus(orderNo string, currentStatus []string, nextStatus string) (bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// 存在確認と現在の状態チェック
	var count int
	// 可変長のステータス条件組み立て
	queryCheck := `SELECT COUNT(*) FROM order_items WHERE order_no = ?`
	var args []interface{}
	args = append(args, orderNo)
	if len(currentStatus) > 0 {
		queryCheck += " AND order_status IN ("
		for i, s := range currentStatus {
			if i > 0 {
				queryCheck += ","
			}
			queryCheck += "?"
			args = append(args, s)
		}
		queryCheck += ")"
	}

	err = tx.QueryRow(queryCheck, args...).Scan(&count)
	if err != nil || count == 0 {
		return false, err // 該当データなし、またはステータス条件不一致
	}

	// 更新実行
	queryUpdate := `UPDATE order_items SET order_status = ? WHERE order_no = ?`
	_, err = tx.Exec(queryUpdate, nextStatus, orderNo)
	if err != nil {
		return false, err
	}

	err = tx.Commit()
	return true, err
}