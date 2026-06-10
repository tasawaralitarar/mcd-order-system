package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// --- リクエスト・レスポンス用構造体定義 ---

type OrderItemInput struct {
	MenuName  string `json:"menuName"`
	UnitPrice int    `json:"unitPrice"`
	Quantity  int    `json:"quantity"`
	Subtotal  int    `json:"subtotal"`
}

type CreateOrderRequest struct {
	MessageType string           `json:"messageType"`
	TerminalNo  string           `json:"terminalNo"`
	TotalAmount int              `json:"totalAmount"`
	Items       []OrderItemInput `json:"items"`
}

type CreateOrderResponse struct {
	Result      string `json:"result"`
	OrderNo     string `json:"orderNo"`
	OrderStatus string `json:"orderStatus,omitempty"`
	TotalAmount int    `json:"totalAmount,omitempty"`
	Message     string `json:"message,omitempty"`
}

type OrderResponseItem struct {
	MenuName  string `json:"menuName"`
	UnitPrice int    `json:"unitPrice"`
	Quantity  int    `json:"quantity"`
	Subtotal  int    `json:"subtotal"`
}

type OrderSummaryResponse struct {
	OrderNo     string              `json:"orderNo"`
	TerminalNo  string              `json:"terminalNo"`
	OrderStatus string              `json:"orderStatus"`
	TotalAmount int                 `json:"totalAmount"`
	Items       []OrderResponseItem `json:"items"`
}

type UpdateStatusRequest struct {
	OrderStatus string `json:"orderStatus"`
}

type GenericResponse struct {
	Result  string `json:"result"`
	Message string `json:"message,omitempty"`
}

type BoardRequest struct {
	TerminalNo  string `json:"terminalNo"`
	MessageType string `json:"messageType"`
	OrderNo     string `json:"orderNo,omitempty"`
}

type BoardResponse struct {
	Result         string   `json:"result"`
	CookingOrders  []string `json:"cookingOrders"`
	ReadyOrders    []string `json:"readyOrders"`
}

type KitchenRequest struct {
	TerminalNo  string `json:"terminalNo,omitempty"`
	MessageType string `json:"messageType"`
	OrderNo     string `json:"orderNo,omitempty"`
}

type KitchenOrderSummary struct {
	OrderNo string               `json:"orderNo"`
	Items   []KitchenItemSummary `json:"items"`
}

type KitchenItemSummary struct {
	MenuName string `json:"menuName"`
	Quantity int    `json:"quantity"`
}

type KitchenResponse struct {
	Result string                `json:"result"`
	Orders []KitchenOrderSummary `json:"orders"`
}

// --- ミドルウェア機能 ---

// 全てのエンドポイントで必須となるCORS対応
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		// プリフライトリクエストの即時返却
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// 汎用エラーJSONレスポンスユーティリティ
func respondWithError(w http.ResponseWriter, code int, msg string) {
	logger.Printf("[API出電文] エラーレスポンス返却 Status:%d, Message:%s", code, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(GenericResponse{Result: "NG", Message: msg})
}

// 汎用正常JSONレスポンスユーティリティ
func respondWithJSON(w http.ResponseWriter, code int, payload interface{}, logMsg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(payload)
	
	// ログ出力用に構造体をダンプ
	payloadBytes, _ := json.Marshal(payload)
	logger.Printf("[API出電文] %s Payload:%s", logMsg, string(payloadBytes))
}

// --- ハンドラ実装 ---

// 3.1 POST /api/orders : 注文電文受信
func handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	var req CreateOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, http.StatusBadRequest, "JSONパースエラー")
		return
	}

	reqBytes, _ := json.Marshal(req)
	logger.Printf("[API入電文] POST /api/orders: %s", string(reqBytes))

	// バリデーションチェック
	if req.TerminalNo == "" {
		respondWithError(w, http.StatusBadRequest, "terminalNoは必須です")
		return
	}
	if req.MessageType != "ORDER_CONFIRM" {
		respondWithError(w, http.StatusBadRequest, "messageTypeが不正です")
		return
	}
	if req.TotalAmount < 1 {
		respondWithError(w, http.StatusBadRequest, "totalAmountは1以上である必要があります")
		return
	}
	if len(req.Items) < 1 || len(req.Items) > 5 {
		respondWithError(w, http.StatusBadRequest, "itemsは1〜5件の範囲で指定してください")
		return
	}

	calcTotalAmount := 0
	menuMap := make(map[string]bool)

	for _, item := range req.Items {
		if item.MenuName == "" {
			respondWithError(w, http.StatusBadRequest, "menuNameは必須です")
			return
		}
		if item.UnitPrice < 1 {
			respondWithError(w, http.StatusBadRequest, "unitPriceは1以上である必要があります")
			return
		}
		if item.Quantity < 1 || item.Quantity > 5 {
			respondWithError(w, http.StatusBadRequest, "quantityは1〜5の範囲で指定してください")
			return
		}
		if menuMap[item.MenuName] {
			respondWithError(w, http.StatusBadRequest, fmt.Sprintf("menuNameの重複は禁止されています: %s", item.MenuName))
			return
		}
		menuMap[item.MenuName] = true

		// サブトータルの整合性検証
		expectedSubtotal := item.UnitPrice * item.Quantity
		if item.Subtotal != expectedSubtotal {
			respondWithError(w, http.StatusBadRequest, fmt.Sprintf("subtotalの計算が一致しません。期待値: %d, 入力値: %d", expectedSubtotal, item.Subtotal))
			return
		}
		calcTotalAmount += item.Subtotal
	}

	if req.TotalAmount != calcTotalAmount {
		respondWithError(w, http.StatusBadRequest, fmt.Sprintf("totalAmountが明細の合計と一致しません。期待値: %d, 入力値: %d", calcTotalAmount, req.TotalAmount))
		return
	}

	// トランザクション処理による採番とデータ保存
	orderNo, err := saveOrderTx(req.TerminalNo, req.Items)
	if err != nil {
		logger.Printf("[DBエラー] 注文登録失敗: %v", err)
		respondWithError(w, http.StatusInternalServerError, "注文の登録に失敗しました")
		return
	}

	logger.Printf("[DB登録内容] 注文確定成功 OrderNo:%s, TerminalNo:%s, TotalAmount:%d", orderNo, req.TerminalNo, req.TotalAmount)

	resp := CreateOrderResponse{
		Result:      "OK",
		OrderNo:     orderNo,
		OrderStatus: StatusReceived,
		TotalAmount: req.TotalAmount,
		Message:     "オーダを受信しました",
	}
	respondWithJSON(w, http.StatusCreated, resp, "注文登録成功レスポンス")
}

// 3.1 GET /api/orders & GET /api/orders?status=xxx : 注文一覧・状態別一覧取得
func handleListOrders(w http.ResponseWriter, r *http.Request) {
	logger.Printf("[API入電文] GET /api/orders (Query: %s)", r.URL.RawQuery)
	statusFilter := r.URL.Query().Get("status")

	query := `SELECT id, order_no, terminal_no, order_status, item_no, menu_name, unit_price, quantity, subtotal FROM order_items`
	var args []interface{}
	if statusFilter != "" {
		query += ` WHERE order_status = ?`
		args = append(args, statusFilter)
	}
	query += ` ORDER BY id ASC`

	rows, err := db.Query(query, args...)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "データ取得に失敗しました")
		return
	}
	defer rows.Close()

	// 注文番号単位で集約処理を行うためのマップと順序維持のスライス
	orderMap := make(map[string]*OrderSummaryResponse)
	var orderOrder []string

	for rows.Next() {
		var oNo, tNo, status, mName string
		var id, itemNo, uPrice, qty, sub int
		err := rows.Scan(&id, &oNo, &tNo, &status, &itemNo, &mName, &uPrice, &qty, &sub)
		if err != nil {
			respondWithError(w, http.StatusInternalServerError, "データパースに失敗しました")
			return
		}

		if _, exists := orderMap[oNo]; !exists {
			orderMap[oNo] = &OrderSummaryResponse{
				OrderNo:     oNo,
				TerminalNo:  tNo,
				OrderStatus: status,
				TotalAmount: 0,
				Items:       []OrderResponseItem{},
			}
			orderOrder = append(orderOrder, oNo)
		}

		orderMap[oNo].TotalAmount += sub
		orderMap[oNo].Items = append(orderMap[oNo].Items, OrderResponseItem{
			MenuName:  mName,
			UnitPrice: uPrice,
			Quantity:  qty,
			Subtotal:  sub,
		})
	}

	resultList := make([]OrderSummaryResponse, 0)
	for _, oNo := range orderOrder {
		resultList = append(resultList, *orderMap[oNo])
	}

	respondWithJSON(w, http.StatusOK, resultList, "注文一覧取得完了")
}

// 3.1 GET /api/orders/{orderNo} : 注文詳細取得
func handleGetOrder(w http.ResponseWriter, r *http.Request) {
	// パスパラメータの取得 (Go 1.22+ 標準機能)
	orderNo := r.PathValue("orderNo")
	logger.Printf("[API入電文] GET /api/orders/%s", orderNo)

	query := `SELECT order_no, terminal_no, order_status, menu_name, unit_price, quantity, subtotal FROM order_items WHERE order_no = ? ORDER BY item_no ASC`
	rows, err := db.Query(query, orderNo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "データ詳細取得に失敗しました")
		return
	}
	defer rows.Close()

	var summary *OrderSummaryResponse

	for rows.Next() {
		var oNo, tNo, status, mName string
		var uPrice, qty, sub int
		if err := rows.Scan(&oNo, &tNo, &status, &mName, &uPrice, &qty, &sub); err != nil {
			respondWithError(w, http.StatusInternalServerError, "明細パースに失敗しました")
			return
		}

		if summary == nil {
			summary = &OrderSummaryResponse{
				OrderNo:     oNo,
				TerminalNo:  tNo,
				OrderStatus: status,
				TotalAmount: 0,
				Items:       []OrderResponseItem{},
			}
		}
		summary.TotalAmount += sub
		summary.Items = append(summary.Items, OrderResponseItem{
			MenuName:  mName,
			UnitPrice: uPrice,
			Quantity:  qty,
			Subtotal:  sub,
		})
	}

	if summary == nil {
		respondWithError(w, http.StatusNotFound, "指定された注文番号が見つかりません")
		return
	}

	respondWithJSON(w, http.StatusOK, summary, "注文詳細取得完了")
}

// 3.1 PUT /api/orders/{orderNo}/status : 注文状態変更
func handleUpdateOrderStatus(w http.ResponseWriter, r *http.Request) {
	orderNo := r.PathValue("orderNo")
	var req UpdateStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, http.StatusBadRequest, "JSONパースエラー")
		return
	}

	logger.Printf("[API入電文] PUT /api/orders/%s/status TargetStatus:%s", orderNo, req.OrderStatus)

	if req.OrderStatus != StatusReceived && req.OrderStatus != StatusCooking && req.OrderStatus != StatusDelivered {
		respondWithError(w, http.StatusBadRequest, "不適切なステータス値です")
		return
	}

	// 状態制限なしの無条件アップデート（管理・デバッグ用途等）
	updated, err := updateStatus(orderNo, nil, req.OrderStatus)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "ステータス更新に失敗しました")
		return
	}

	if !updated {
		respondWithError(w, http.StatusNotFound, "対象の注文が存在しません")
		return
	}

	logger.Printf("[DB更新内容] ステータス強制更新完了 OrderNo:%s -> %s", orderNo, req.OrderStatus)
	respondWithJSON(w, http.StatusOK, GenericResponse{Result: "OK"}, "注文状態変更完了")
}

// 3.2 POST /api/board : フロント掲示板機能
func handleBoardAction(w http.ResponseWriter, r *http.Request) {
	var req BoardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, http.StatusBadRequest, "JSONパースエラー")
		return
	}

	reqBytes, _ := json.Marshal(req)
	logger.Printf("[API入電文] POST /api/board: %s", string(reqBytes))

	if req.MessageType != "BOARD_REQUEST" {
		respondWithError(w, http.StatusBadRequest, "messageTypeが不正です")
		return
	}

	// orderNo が指定されている場合は「受け渡し完了処理」を実行
	if req.OrderNo != "" {
		// 「調理済み」からのみ「受け渡し済み」への変更を許可する
		updated, err := updateStatus(req.OrderNo, []string{StatusCooking}, StatusDelivered)
		if err != nil {
			respondWithError(w, http.StatusInternalServerError, "受け渡し完了処理に失敗しました")
			return
		}
		if !updated {
			respondWithError(w, http.StatusBadRequest, "対象の注文が存在しないか、ステータスが「調理済み」ではありません")
			return
		}
		logger.Printf("[DB更新内容] 掲示板契機受け渡し完了更新 OrderNo:%s -> %s", req.OrderNo, StatusDelivered)
	}

	// 最新の掲示板用一覧を構築して返却
	cookingOrders, err := getOrderNosByStatus(StatusReceived)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "データ取得に失敗しました")
		return
	}
	readyOrders, err := getOrderNosByStatus(StatusCooking)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "データ取得に失敗しました")
		return
	}

	resp := BoardResponse{
		Result:        "OK",
		CookingOrders: cookingOrders,
		ReadyOrders:   readyOrders,
	}
	respondWithJSON(w, http.StatusOK, resp, "掲示板最新情報返却")
}

// 3.3 POST /api/kitchen : 厨房機能
func handleKitchenAction(w http.ResponseWriter, r *http.Request) {
	var req KitchenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, http.StatusBadRequest, "JSONパースエラー")
		return
	}

	reqBytes, _ := json.Marshal(req)
	logger.Printf("[API入電文] POST /api/kitchen: %s", string(reqBytes))

	if req.MessageType != "KITCHEN_REQUEST" {
		respondWithError(w, http.StatusBadRequest, "messageTypeが不正です")
		return
	}

	// orderNo が指定されている場合は「調理完了処理」を実行
	if req.OrderNo != "" {
		// 「オーダ受信済み」からのみ「調理済み」への変更を許可する
		updated, err := updateStatus(req.OrderNo, []string{StatusReceived}, StatusCooking)
		if err != nil {
			respondWithError(w, http.StatusInternalServerError, "調理完了処理に失敗しました")
			return
		}
		if !updated {
			respondWithError(w, http.StatusBadRequest, "対象の注文が存在しないか、すでに調理中・調理完了状態です")
			return
		}
		logger.Printf("[DB更新内容] 厨房契機調理完了更新 OrderNo:%s -> %s", req.OrderNo, StatusCooking)
	}

	// 未調理（オーダ受信済み）の一覧と詳細を再構築して返却
	query := `SELECT order_no, menu_name, quantity FROM order_items WHERE order_status = ? ORDER BY id ASC`
	rows, err := db.Query(query, StatusReceived)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "厨房データ取得に失敗しました")
		return
	}
	defer rows.Close()

	kitchenMap := make(map[string]*KitchenOrderSummary)
	var orderOrder []string

	for rows.Next() {
		var oNo, mName string
		var qty int
		if err := rows.Scan(&oNo, &mName, &qty); err != nil {
			respondWithError(w, http.StatusInternalServerError, "厨房データパースエラー")
			return
		}

		if _, exists := kitchenMap[oNo]; !exists {
			kitchenMap[oNo] = &KitchenOrderSummary{
				OrderNo: oNo,
				Items:   []KitchenItemSummary{},
			}
			orderOrder = append(orderOrder, oNo)
		}
		kitchenMap[oNo].Items = append(kitchenMap[oNo].Items, KitchenItemSummary{
			MenuName: mName,
			Quantity: qty,
		})
	}

	ordersResp := make([]KitchenOrderSummary, 0)
	for _, oNo := range orderOrder {
		ordersResp = append(ordersResp, *kitchenMap[oNo])
	}

	resp := KitchenResponse{
		Result: "OK",
		Orders: ordersResp,
	}
	respondWithJSON(w, http.StatusOK, resp, "厨房最新情報返却")
}