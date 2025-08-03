# --- Tahap Build ---
# Menggunakan image Go resmi sebagai basis untuk kompilasi
FROM golang:1.24.5-alpine3.22 AS builder

# Set direktori kerja di dalam kontainer
WORKDIR /app

# Salin file manajemen dependensi
COPY go.mod ./
COPY go.sum ./
# Download dependensi
RUN go mod download

# Salin sisa source code
COPY . .

# Compile aplikasi Go menjadi satu binary statis
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /app/main .

# --- Tahap Final ---
# Menggunakan image alpine yang sangat kecil sebagai basis
FROM alpine:latest

WORKDIR /app

# Salin binary yang sudah di-compile dari tahap build
COPY --from=builder /app/main .

# Perintah untuk menjalankan binary saat kontainer dimulai
CMD ["./main"]