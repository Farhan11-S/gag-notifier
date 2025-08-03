package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	pb "gag-listener/grow_api"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// Struct untuk parsing rare-items.json
type RareItemsConfig struct {
	Seeds []string `json:"seeds"`
	Gear  []string `json:"gear"`
	Eggs  []string `json:"eggs"`
}

var (
	// Global Data Store
	latestData = map[string]interface{}{
		"weather":   map[string]interface{}{},
		"gear":      []interface{}{},
		"seeds":     []interface{}{},
		"eggs":      []interface{}{},
		"honey":     []interface{}{},
		"cosmetics": []interface{}{},
		"timestamp": int64(0),
	}
	mutex = &sync.RWMutex{}

	// Notifier untuk item langka
	rareSeedSet = make(map[string]bool)
	rareGearSet = make(map[string]bool)
	rareEggSet  = make(map[string]bool)

	// Channel generik untuk semua notifikasi
	notificationChannel = make(chan *pb.RareItemNotification, 10)
)

// grpcServer adalah implementasi dari service gRPC kita
type grpcServer struct {
	pb.UnimplementedGrowAPIServiceServer
}

// Implementasi RPC stream generik yang baru
func (s *grpcServer) SubscribeToRareItemNotifications(in *emptypb.Empty, stream pb.GrowAPIService_SubscribeToRareItemNotificationsServer) error {
	log.Println("Klien baru berlangganan notifikasi item langka.")
	for {
		select {
		case <-stream.Context().Done():
			log.Println("Klien notifikasi terputus.")
			return nil
		case notification := <-notificationChannel:
			if err := stream.Send(notification); err != nil {
				log.Printf("Gagal mengirim notifikasi ke klien: %v", err)
				return err
			}
		}
	}
}

// Implementasi RPC standar
func (s *grpcServer) GetAllData(ctx context.Context, in *emptypb.Empty) (*pb.AllDataResponse, error) {
	mutex.RLock()
	defer mutex.RUnlock()
	weatherStruct, _ := structpb.NewStruct(latestData["weather"].(map[string]interface{}))
	return &pb.AllDataResponse{
		Weather:   weatherStruct,
		Gear:      convertToProtoItems(latestData["gear"]),
		Seeds:     convertToProtoItems(latestData["seeds"]),
		Eggs:      convertToProtoItems(latestData["eggs"]),
		Honey:     convertToProtoItems(latestData["honey"]),
		Cosmetics: convertToProtoItems(latestData["cosmetics"]),
		Timestamp: latestData["timestamp"].(int64),
	}, nil
}
func (s *grpcServer) GetGear(ctx context.Context, in *emptypb.Empty) (*pb.StockResponse, error) {
	mutex.RLock()
	defer mutex.RUnlock()
	return &pb.StockResponse{Items: convertToProtoItems(latestData["gear"])}, nil
}
func (s *grpcServer) GetSeeds(ctx context.Context, in *emptypb.Empty) (*pb.StockResponse, error) {
	mutex.RLock()
	defer mutex.RUnlock()
	return &pb.StockResponse{Items: convertToProtoItems(latestData["seeds"])}, nil
}
func (s *grpcServer) GetCosmetics(ctx context.Context, in *emptypb.Empty) (*pb.StockResponse, error) {
	mutex.RLock()
	defer mutex.RUnlock()
	return &pb.StockResponse{Items: convertToProtoItems(latestData["cosmetics"])}, nil
}
func (s *grpcServer) GetEventShop(ctx context.Context, in *emptypb.Empty) (*pb.StockResponse, error) {
	mutex.RLock()
	defer mutex.RUnlock()
	return &pb.StockResponse{Items: convertToProtoItems(latestData["honey"])}, nil
}
func (s *grpcServer) GetEggs(ctx context.Context, in *emptypb.Empty) (*pb.EggResponse, error) {
	mutex.RLock()
	defer mutex.RUnlock()
	return &pb.EggResponse{Eggs: convertToProtoItems(latestData["eggs"])}, nil
}
func (s *grpcServer) GetWeather(ctx context.Context, in *emptypb.Empty) (*pb.WeatherResponse, error) {
	mutex.RLock()
	defer mutex.RUnlock()
	var weatherStruct *structpb.Struct
	if weatherData, ok := latestData["weather"].(map[string]interface{}); ok {
		weatherStruct, _ = structpb.NewStruct(weatherData)
	} else {
		weatherStruct, _ = structpb.NewStruct(map[string]interface{}{})
	}
	return &pb.WeatherResponse{Weather: weatherStruct}, nil
}

// --- Fungsi Helper dan Logika Bisnis ---

// Membaca file JSON baru dan memuat semua item langka
func loadRareItems() {
	file, err := ioutil.ReadFile("rare-items.json")
	if err != nil {
		log.Fatalf("Gagal membaca rare-items.json: %v", err)
	}
	var config RareItemsConfig
	if err := json.Unmarshal(file, &config); err != nil {
		log.Fatalf("Gagal parsing rare-items.json: %v", err)
	}
	for _, seed := range config.Seeds {
		rareSeedSet[seed] = true
	}
	for _, gear := range config.Gear {
		rareGearSet[gear] = true
	}
	for _, egg := range config.Eggs {
		rareEggSet[egg] = true
	}
	log.Printf("Berhasil memuat %d benih, %d gear, dan %d telur langka.", len(rareSeedSet), len(rareGearSet), len(rareEggSet))
}

// Fungsi generik untuk mengecek item langka
func checkForRareItems(category string, items interface{}, rareItemSet map[string]bool) {
	if items == nil {
		return
	}
	// Gunakan type switch untuk menangani data secara fleksibel
	switch typedItems := items.(type) {
	case []interface{}:
		for _, item := range typedItems {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if name, nameOk := itemMap["name"].(string); nameOk {
					if rareItemSet[name] {
						log.Printf("ITEM LANGKA DITEMUKAN: Kategori=%s, Nama=%s", category, name)
						quantity, _ := getFloat64(itemMap["quantity"])
						notificationChannel <- &pb.RareItemNotification{
							ItemName:  name,
							Quantity:  quantity,
							Timestamp: time.Now().Unix(),
							Category:  category,
						}
					}
				}
			}
		}
	default:
		log.Printf("Tipe data tidak terduga untuk kategori %s: %T", category, items)
	}
}

// processWebSocketMessage memproses pesan dari WebSocket
func processWebSocketMessage(message []byte) {
	var incomingData map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(string(message)))
	decoder.UseNumber() // Gunakan json.Number agar tipe angka tidak hilang
	if err := decoder.Decode(&incomingData); err != nil {
		log.Printf("JSON unmarshal error: %v", err)
		return
	}

	mutex.Lock()
	defer mutex.Unlock()

	var dataMap map[string]interface{}
	if data, ok := incomingData["data"].(map[string]interface{}); ok {
		dataMap = data
		for k, v := range dataMap {
			latestData[k] = v
		}
	}
	latestData["timestamp"] = time.Now().Unix()

	// Cek semua kategori item langka
	checkForRareItems("seeds", dataMap["seeds"], rareSeedSet)
	checkForRareItems("gear", dataMap["gear"], rareGearSet)
	checkForRareItems("eggs", dataMap["eggs"], rareEggSet)

	// Jalankan pembersihan data
	for _, category := range []string{"gear", "seeds", "cosmetics", "honey"} {
		if items, ok := latestData[category].([]interface{}); ok {
			latestData[category] = cleanItems(items)
		}
	}
	if eggs, ok := latestData["eggs"].([]interface{}); ok {
		latestData["eggs"] = combineItemsByName(eggs)
	}

	log.Println("Global data updated.")
}

// --- Fungsi Konversi dan Utility ---

// Helper untuk konversi angka yang fleksibel
func getFloat64(v interface{}) (float64, bool) {
	if f, ok := v.(float64); ok {
		return f, true
	}
	if i, ok := v.(int); ok {
		return float64(i), true
	}
	if i, ok := v.(int64); ok {
		return float64(i), true
	}
	if n, ok := v.(json.Number); ok {
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

// Fungsi konversi dari data Go ke Protobuf
func convertToProtoItems(data interface{}) []*pb.Item {
	var protoItems []*pb.Item
	switch items := data.(type) {
	case []map[string]interface{}:
		for _, itemMap := range items {
			name, nameOk := itemMap["name"].(string)
			quantity, quantityOk := getFloat64(itemMap["quantity"])
			if nameOk && quantityOk {
				protoItems = append(protoItems, &pb.Item{Name: name, Quantity: quantity})
			}
		}
	case []interface{}:
		for _, item := range items {
			if itemMap, ok := item.(map[string]interface{}); ok {
				name, nameOk := itemMap["name"].(string)
				quantity, quantityOk := getFloat64(itemMap["quantity"])
				if nameOk && quantityOk {
					protoItems = append(protoItems, &pb.Item{Name: name, Quantity: quantity})
				}
			}
		}
	}
	return protoItems
}

func cleanItems(items []interface{}) []interface{} {
	cleaned := make([]interface{}, 0, len(items))
	keysToKeep := map[string]bool{"name": true, "quantity": true}
	for _, item := range items {
		if itemMap, ok := item.(map[string]interface{}); ok {
			newItem := make(map[string]interface{})
			for key, value := range itemMap {
				if keysToKeep[key] {
					newItem[key] = value
				}
			}
			cleaned = append(cleaned, newItem)
		}
	}
	return cleaned
}

func combineItemsByName(items []interface{}) []interface{} {
	combined := make(map[string]float64)
	for _, item := range items {
		if itemMap, ok := item.(map[string]interface{}); ok {
			if name, ok := itemMap["name"].(string); ok {
				if quantity, ok := getFloat64(itemMap["quantity"]); ok {
					combined[name] += quantity
				}
			}
		}
	}
	result := make([]interface{}, 0, len(combined))
	for name, qty := range combined {
		result = append(result, map[string]interface{}{"name": name, "quantity": qty})
	}
	return result
}

// --- WebSocket Listener ---

func websocketListener() {
	uri := "wss://ws.growagardenpro.com/"
	for {
		conn, _, err := websocket.DefaultDialer.Dial(uri, nil)
		if err != nil {
			log.Printf("WebSocket dial error: %v. Retrying in 5s...", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Println("API WebSocket Connected.")
		func() {
			defer conn.Close()
			for {
				_, message, err := conn.ReadMessage()
				if err != nil {
					log.Printf("WebSocket read error: %v. Reconnecting...", err)
					return
				}
				processWebSocketMessage(message)
			}
		}()
	}
}

// --- Main Function ---

func main() {
	// Setup logging ke file
	file, err := os.OpenFile("/app/logs/service.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("Gagal membuka file log: %v", err)
	}
	defer file.Close()
	log.SetOutput(file)

	// Muat konfigurasi item langka saat startup
	loadRareItems()

	// Jalankan WebSocket listener di background
	go websocketListener()

	// Setup listener TCP untuk gRPC server
	port := ":50051"
	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("Gagal listen: %v", err)
	}

	// Buat instance gRPC server baru
	s := grpc.NewServer()

	// Daftarkan implementasi service kita ke server
	pb.RegisterGrowAPIServiceServer(s, &grpcServer{})

	log.Printf("gRPC server berjalan di port %v", port)

	// Mulai melayani permintaan
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Gagal menjalankan server: %v", err)
	}
}
