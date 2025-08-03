package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	// PASTIKAN ANDA MENGGANTI 'discord-bot' DENGAN NAMA MODUL ANDA
	pb "gag-discord-bot/grow_api"

	"github.com/bwmarrin/discordgo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

var (
	botToken   string
	channelID  string // Diperlukan untuk notifikasi benih langka
	grpcClient pb.GrowAPIServiceClient
	dg         *discordgo.Session

	notificationCache      = make(map[string]time.Time)
	notificationCacheMutex = &sync.Mutex{}
)

// Daftar slash command
var commands = []*discordgo.ApplicationCommand{
	{Name: "ping", Description: "Uji coba koneksi ke bot."},
	{Name: "gear", Description: "Cek stok gear saat ini."},
	{Name: "seeds", Description: "Cek stok benih saat ini."},
	{Name: "eggs", Description: "Cek stok telur saat ini."},
	{Name: "eventshop", Description: "Cek stok event shop saat ini."},
	{Name: "cosmetics", Description: "Cek stok kosmetik saat ini."},
}

// Map untuk menghubungkan nama command dengan fungsi handler-nya
var commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
	"ping": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "Pong!"},
		})
	},
	"gear": handleStockCommand("Gear", 0x3498db, func() (*pb.StockResponse, error) {
		return grpcClient.GetGear(context.Background(), &emptypb.Empty{})
	}),
	"seeds": handleStockCommand("Benih", 0x2ecc71, func() (*pb.StockResponse, error) {
		return grpcClient.GetSeeds(context.Background(), &emptypb.Empty{})
	}),
	"cosmetics": handleStockCommand("Kosmetik", 0x9b59b6, func() (*pb.StockResponse, error) {
		return grpcClient.GetCosmetics(context.Background(), &emptypb.Empty{})
	}),
	"eventshop": handleStockCommand("Event Shop", 0xf1c40f, func() (*pb.StockResponse, error) {
		return grpcClient.GetEventShop(context.Background(), &emptypb.Empty{})
	}),
	"eggs": handleStockCommand("Telur", 0xe74c3c, func() (*pb.StockResponse, error) {
		res, err := grpcClient.GetEggs(context.Background(), &emptypb.Empty{})
		if err != nil {
			return nil, err
		}
		return &pb.StockResponse{Items: res.GetEggs()}, nil
	}),
}

// init membaca konfigurasi dari environment variables saat program dimulai.
func init() {
	botToken = os.Getenv("DISCORD_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("Environment variable DISCORD_BOT_TOKEN harus di-set.")
	}
	channelID = os.Getenv("CHANNEL_ID")
	if channelID == "" {
		log.Fatal("Environment variable CHANNEL_ID harus di-set untuk notifikasi.")
	}
}

func main() {
	// --- Setup Koneksi ke gRPC Server ---
	file, err := os.OpenFile("/app/logs/bot.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("Gagal membuka file log: %v", err)
	}
	defer file.Close()
	log.SetOutput(file)

	grpcAddr := "grpc-service:50051"
	conn, err := grpc.Dial(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Gagal terhubung ke gRPC server: %v", err)
	}
	defer conn.Close()
	grpcClient = pb.NewGrowAPIServiceClient(conn)
	log.Println("Berhasil terhubung ke gRPC server di", grpcAddr)

	// --- Setup Bot Discord ---
	// Buat sesi baru, assign ke variabel global 'dg'
	dg, err = discordgo.New("Bot " + botToken)
	if err != nil {
		log.Fatalf("Gagal membuat sesi Discord: %v", err)
	}

	// Tambahkan semua handler yang diperlukan SEBELUM membuka koneksi.
	dg.AddHandler(readyHandler)
	dg.AddHandler(interactionCreateHandler)

	// Buka koneksi WebSocket ke Discord. INI HANYA DIJALANKAN SEKALI.
	err = dg.Open()
	if err != nil {
		log.Fatalf("Gagal membuka koneksi: %v", err)
	}

	// Jalankan subscriber notifikasi di goroutine terpisah SETELAH koneksi terbuka.
	go subscribeToNotifications()
	go cleanupExpiredCache()

	// Tunggu sinyal keluar (Ctrl+C).
	log.Println("Bot sedang berjalan. Tekan CTRL+C untuk keluar.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	// Tutup koneksi Discord dengan rapi.
	log.Println("Mematikan bot.")
	dg.Close()
}

// readyHandler dipanggil saat bot siap, di sini kita mendaftarkan command.
func readyHandler(s *discordgo.Session, event *discordgo.Ready) {
	log.Printf("Bot telah login sebagai: %v#%v", s.State.User.Username, s.State.User.Discriminator)
	log.Println("Mendaftarkan slash commands...")
	registeredCommands, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, "", commands)
	if err != nil {
		log.Fatalf("Gagal mendaftarkan commands: %v", err)
	}
	log.Printf("Berhasil mendaftarkan %d commands.", len(registeredCommands))
}

// interactionCreateHandler adalah router utama untuk semua slash command.
func interactionCreateHandler(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if h, ok := commandHandlers[i.ApplicationCommandData().Name]; ok {
		h(s, i)
	}
}

// --- Logika Notifikasi Benih Langka ---
func subscribeToNotifications() {
	log.Println("Mencoba berlangganan notifikasi benih langka...")
	stream, err := grpcClient.SubscribeToRareItemNotifications(context.Background(), &emptypb.Empty{})
	if err != nil {
		log.Printf("Gagal berlangganan stream, mencoba lagi dalam 10 detik: %v", err)
		time.Sleep(10 * time.Second)
		go subscribeToNotifications()
		return
	}
	log.Println("Berhasil berlangganan. Menunggu notifikasi...")
	for {
		notification, err := stream.Recv()
		if err == io.EOF {
			log.Println("Stream notifikasi ditutup oleh server. Mencoba lagi...")
			time.Sleep(10 * time.Second)
			go subscribeToNotifications()
			return
		}
		if err != nil {
			log.Printf("Error saat menerima notifikasi: %v. Mencoba lagi...", err)
			time.Sleep(10 * time.Second)
			go subscribeToNotifications()
			return
		}
		log.Printf("Notifikasi diterima: Kategori=%s, Nama=%s", notification.GetCategory(), notification.GetItemName())

		sendNotificationMessage(notification)
	}
}

func sendNotificationMessage(notif *pb.RareItemNotification) {
	notificationCacheMutex.Lock()

	cooldown := 5 * time.Minute // Cooldown 5 menit untuk setiap item
	if lastSent, found := notificationCache[notif.GetItemName()]; found && time.Since(lastSent) < cooldown {
		notificationCacheMutex.Unlock()
		log.Printf("Notifikasi untuk '%s' diabaikan (masih dalam masa cooldown).", notif.GetItemName())
		return // Abaikan notifikasi
	}

	notificationCache[notif.GetItemName()] = time.Now()
	notificationCacheMutex.Unlock()

	log.Printf("Mengirim notifikasi baru untuk '%s' ke Discord.", notif.GetItemName())

	var title string
	var color int

	switch notif.GetCategory() {
	case "seeds":
		title = "🚨 Notifikasi Benih Langka! 🚨"
		color = 0xffd700 // Emas
	case "gear":
		title = "✨ Notifikasi Gear Langka! ✨"
		color = 0x3498db // Biru
	case "eggs":
		title = "🥚 Notifikasi Telur Langka! 🥚"
		color = 0x9b59b6 // Ungu
	default:
		title = "🔔 Notifikasi Item Langka! 🔔"
		color = 0x95a5a6 // Abu-abu
	}

	embed := &discordgo.MessageEmbed{
		Title:       title,
		Description: fmt.Sprintf("**%s** sekarang tersedia di toko!", notif.GetItemName()),
		Color:       color,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Jumlah", Value: fmt.Sprintf("%d", int(notif.GetQuantity())), Inline: true},
		},
		Timestamp: time.Unix(notif.GetTimestamp(), 0).Format(time.RFC3339),
		Footer:    &discordgo.MessageEmbedFooter{Text: "Rare Item Notifier"},
	}

	_, err := dg.ChannelMessageSendEmbed(channelID, embed)
	if err != nil {
		log.Printf("Gagal mengirim pesan notifikasi ke Discord: %v", err)
	}
}

// --- Handler dan Formatter untuk Slash Command ---
func handleStockCommand(title string, color int, grpcCall func() (*pb.StockResponse, error)) func(s *discordgo.Session, i *discordgo.InteractionCreate) {
	return func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredChannelMessageWithSource})
		res, err := grpcCall()
		if err != nil {
			log.Printf("gRPC call gagal untuk command '%s': %v", title, err)
			sendErrorResponse(s, i, "Gagal mengambil data dari backend service.")
			return
		}
		embed := formatItemsToEmbed(title, res.GetItems(), color)
		_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
		if err != nil {
			log.Printf("Gagal mengedit respons interaksi: %v", err)
		}
	}
}

func formatItemsToEmbed(title string, items []*pb.Item, color int) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:     fmt.Sprintf("🌱 Stok %s Saat Ini", title),
		Color:     color,
		Timestamp: time.Now().Format(time.RFC3339),
		Footer:    &discordgo.MessageEmbedFooter{Text: "Grow a Garden API Bot"},
	}
	if len(items) == 0 {
		embed.Description = "Stok saat ini kosong."
		return embed
	}
	maxFields := 25
	if len(items) < maxFields {
		maxFields = len(items)
	}
	fields := make([]*discordgo.MessageEmbedField, maxFields)
	for i := 0; i < maxFields; i++ {
		fields[i] = &discordgo.MessageEmbedField{
			Name:   items[i].GetName(),
			Value:  fmt.Sprintf("Jumlah: %d", int(items[i].GetQuantity())),
			Inline: true,
		}
	}
	embed.Fields = fields
	return embed
}

func sendErrorResponse(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &message})
}

func cleanupExpiredCache() {
	ticker := time.NewTicker(20 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		notificationCacheMutex.Lock()
		log.Println("Membersihkan cache notifikasi yang sudah lama...")
		for itemName, lastSent := range notificationCache {
			// Hapus entri yang lebih lama dari 24 jam
			if time.Since(lastSent) > 24*time.Hour {
				delete(notificationCache, itemName)
			}
		}
		notificationCacheMutex.Unlock()
	}
}
