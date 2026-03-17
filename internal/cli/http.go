package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

// getJSON performs a GET request and returns the parsed JSON response.
func getJSON(url string) (map[string]interface{}, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("cannot reach hub: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	return data, nil
}

// postJSON performs a POST request with JSON body and returns the parsed response.
func postJSON(url string, payload interface{}) (map[string]interface{}, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cannot reach hub: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var errData map[string]interface{}
		json.Unmarshal(respBody, &errData)
		if detail, ok := errData["detail"].(string); ok {
			return nil, fmt.Errorf("%s (HTTP %d)", detail, resp.StatusCode)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var data map[string]interface{}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	return data, nil
}

// str extracts a string from a map, returning "" for nil.
func str(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return fmt.Sprint(v)
}

// num extracts a float64 from a map, returning 0 for nil.
func num(m map[string]interface{}, key string) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	f, ok := v.(float64)
	if ok {
		return f
	}
	return 0
}

// items extracts a slice of maps from a JSON response key.
func items(data map[string]interface{}, key string) []map[string]interface{} {
	v, ok := data[key]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]interface{}); ok {
			result = append(result, m)
		}
	}
	return result
}
