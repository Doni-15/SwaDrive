package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

const (
	defaultHealthURL = "http://127.0.0.1:8080/api/v1/health"
	healthTimeout    = 3 * time.Second
)

func main() {
	url := os.Getenv("SWADRIVE_HEALTHCHECK_URL")
	if url == "" {
		url = defaultHealthURL
	}
	client := &http.Client{Timeout: healthTimeout}
	if err := check(context.Background(), client, url); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "SwaDrive health check failed")
		os.Exit(1)
	}
}

func check(ctx context.Context, client *http.Client, url string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("health endpoint returned a non-success status")
	}
	return nil
}
