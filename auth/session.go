package auth

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
)

var TOKEN_EXPIRE_TIME_SEC int

func parseTokenExpireTime() {
	duration := os.Getenv("TOKEN_EXPIRE_TIME")
	if duration == "never" || duration == "0" || duration == "" {
		TOKEN_EXPIRE_TIME_SEC = 0
	} else {
		parse, err := time.ParseDuration(os.Getenv("TOKEN_EXPIRE_TIME"))
		if err != nil {
			fmt.Printf("failed to parse token expire time: %v\n", err)
			os.Exit(1)
		}

		TOKEN_EXPIRE_TIME_SEC = int(parse.Seconds())
	}
}

func Init() {
	var err error
	publicKey, privateKey, err = ed25519.GenerateKey(nil)
	if err != nil {
		fmt.Printf("failed to generate ed25519 key pair: %v\n", err)
		os.Exit(1)
	}

	parseTokenExpireTime()
}

func InitFromPath(privatePath, publicPath string) error {
	privateKeyData, err := os.ReadFile(privatePath)
	if err != nil {
		return fmt.Errorf("failed to read private key file: %w", err)
	}

	publicKeyData, err := os.ReadFile(publicPath)
	if err != nil {
		return fmt.Errorf("failed to read public key file: %w", err)
	}

	privateKey = ed25519.PrivateKey(privateKeyData)
	publicKey = ed25519.PublicKey(publicKeyData)

	parseTokenExpireTime()

	return nil
}

func CreateJWT(userID string) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(time.Hour * 72).Unix(),
	})

	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

func AuthenticateJWT(token string) (string, error) {
	t, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return publicKey, nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to parse token: %w", err)
	}

	if !t.Valid {
		return "", fmt.Errorf("invalid token")
	}

	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("failed to parse claims")
	}

	userID, ok := claims["sub"].(string)
	if !ok {
		return "", fmt.Errorf("failed to parse user ID")
	}

	return userID, nil
}
