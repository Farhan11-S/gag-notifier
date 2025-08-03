package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	pb "gag-rest-api-gateway/grow_api"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

var (
	// gRPC client yang akan kita gunakan untuk berkomunikasi dengan service backend
	grpcClient pb.GrowAPIServiceClient
)

// --- Helper untuk mengirim response JSON ---
func respondWithProtoJSON(w http.ResponseWriter, m proto.Message) {
	marshaler := protojson.MarshalOptions{
		UseProtoNames:   false, // Gunakan camelCase untuk nama field JSON
		EmitUnpopulated: true,
	}
	jsonBytes, err := marshaler.Marshal(m)
	if err != nil {
		log.Printf("Gagal marshal proto ke JSON: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(jsonBytes)
}

// --- Middleware (Rate Limiter & CORS) ---
var visitors = make(map[string]*rate.Limiter)
var visitorsMutex = &sync.Mutex{}

func getVisitorLimiter(ip string) *rate.Limiter {
	visitorsMutex.Lock()
	defer visitorsMutex.Unlock()
	limiter, exists := visitors[ip]
	if !exists {
		limiter = rate.NewLimiter(rate.Every(1*time.Second), 1) // 60 req/menit
		visitors[ip] = limiter
	}
	return limiter
}

func rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := strings.Split(r.RemoteAddr, ":")[0]
		limiter := getVisitorLimiter(ip)
		if !limiter.Allow() {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- HTTP Handlers ---
func rootHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "REST API Gateway is online"}`))
}

func alldataHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()

	res, err := grpcClient.GetAllData(ctx, &emptypb.Empty{})
	if err != nil {
		log.Printf("gRPC call ke GetAllData gagal: %v", err)
		http.Error(w, "Failed to fetch data from backend service", http.StatusServiceUnavailable)
		return
	}
	respondWithProtoJSON(w, res)
}

// createGenericHandler membuat handler HTTP yang memanggil gRPC dan merespons dengan JSON
func createGenericHandler(grpcCall func(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (proto.Message, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()

		res, err := grpcCall(ctx, &emptypb.Empty{})
		if err != nil {
			log.Printf("gRPC call gagal: %v", err)
			http.Error(w, "Failed to fetch data from backend service", http.StatusServiceUnavailable)
			return
		}
		respondWithProtoJSON(w, res)
	}
}

// --- Main Function ---
func main() {
	// Setup logging ke file
	file, err := os.OpenFile("/app/logs/gateway.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("Gagal membuka file log: %v", err)
	}
	defer file.Close()
	log.SetOutput(file)

	// --- Setup Koneksi ke gRPC Server ---
	grpcAddr := "grpc-service:50051"
	conn, err := grpc.Dial(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Gagal terhubung ke gRPC server: %v", err)
	}
	defer conn.Close()
	grpcClient = pb.NewGrowAPIServiceClient(conn)
	log.Println("Berhasil terhubung ke gRPC server di", grpcAddr)

	// --- Setup & Daftarkan HTTP Routes ---
	mux := http.NewServeMux()

	mux.HandleFunc("/", rootHandler)
	mux.HandleFunc("/alldata", alldataHandler)

	mux.HandleFunc("/gear", createGenericHandler(func(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (proto.Message, error) {
		return grpcClient.GetGear(ctx, in, opts...)
	}))
	mux.HandleFunc("/seeds", createGenericHandler(func(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (proto.Message, error) {
		return grpcClient.GetSeeds(ctx, in, opts...)
	}))
	mux.HandleFunc("/cosmetics", createGenericHandler(func(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (proto.Message, error) {
		return grpcClient.GetCosmetics(ctx, in, opts...)
	}))
	mux.HandleFunc("/eventshop", createGenericHandler(func(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (proto.Message, error) {
		return grpcClient.GetEventShop(ctx, in, opts...)
	}))
	mux.HandleFunc("/eggs", createGenericHandler(func(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (proto.Message, error) {
		return grpcClient.GetEggs(ctx, in, opts...)
	}))
	mux.HandleFunc("/weather", createGenericHandler(func(ctx context.Context, in *emptypb.Empty, opts ...grpc.CallOption) (proto.Message, error) {
		return grpcClient.GetWeather(ctx, in, opts...)
	}))

	// Terapkan middleware ke semua handler
	handlerWithMiddleware := corsMiddleware(rateLimitMiddleware(mux))

	// --- Jalankan HTTP Server ---
	port := os.Getenv("API_PORT")
	if port == "" {
		port = "8080" // Default port jika tidak di-set
	}
	
	listenAddr := ":" + port
	log.Printf("REST API Gateway berjalan di http://localhost:%s", port)
	if err := http.ListenAndServe(listenAddr, handlerWithMiddleware); err != nil {
		log.Fatalf("Gagal menjalankan HTTP server: %v", err)
	}
}
