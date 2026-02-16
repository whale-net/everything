package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// SteamWorkshopClient interacts with Steam Workshop API
type SteamWorkshopClient struct {
	apiKey     string
	httpClient *http.Client
}

// WorkshopItemMetadata represents metadata for a workshop item
type WorkshopItemMetadata struct {
	WorkshopID   string
	Title        string
	Description  string
	FileSize     int64
	TimeUpdated  time.Time
	IsCollection bool
}

// CollectionItem represents an item within a collection
type CollectionItem struct {
	WorkshopID string `json:"publishedfileid"`
	Title      string `json:"title"`
}

// NewSteamWorkshopClient creates a new Steam Workshop API client
func NewSteamWorkshopClient(apiKey string, timeout time.Duration) *SteamWorkshopClient {
	return &SteamWorkshopClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// GetWorkshopItemDetails fetches metadata for a workshop item
func (swc *SteamWorkshopClient) GetWorkshopItemDetails(ctx context.Context, workshopID string) (*WorkshopItemMetadata, error) {
	apiURL := "https://api.steampowered.com/ISteamRemoteStorage/GetPublishedFileDetails/v1/"

	data := url.Values{}
	data.Set("itemcount", "1")
	data.Set("publishedfileids[0]", workshopID)

	// Retry logic with exponential backoff
	var resp *http.Response
	var err error
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Create a new request for each retry with the form data
		formReq, reqErr := http.NewRequestWithContext(ctx, "POST", apiURL, nil)
		if reqErr != nil {
			return nil, fmt.Errorf("failed to create request: %w", reqErr)
		}
		formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		formReq.PostForm = data
		
		resp, err = swc.httpClient.Do(formReq)
		if err == nil && resp != nil && resp.StatusCode == http.StatusOK {
			break
		}
		if resp != nil && resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if attempt == maxRetries-1 {
				return nil, fmt.Errorf("steam API returned status %d", resp.StatusCode)
			}
		}
		if err != nil && attempt < maxRetries-1 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch workshop item after retries: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("no response received from steam API")
	}
	defer resp.Body.Close()

	var result struct {
		Response struct {
			PublishedFileDetails []struct {
				PublishedFileID string `json:"publishedfileid"`
				Title           string `json:"title"`
				Description     string `json:"file_description"`
				FileSize        int64  `json:"file_size"`
				TimeUpdated     int64  `json:"time_updated"`
				FileType        int    `json:"file_type"` // 2 = collection
			} `json:"publishedfiledetails"`
		} `json:"response"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Response.PublishedFileDetails) == 0 {
		return nil, fmt.Errorf("workshop item not found")
	}

	item := result.Response.PublishedFileDetails[0]
	return &WorkshopItemMetadata{
		WorkshopID:   item.PublishedFileID,
		Title:        item.Title,
		Description:  item.Description,
		FileSize:     item.FileSize,
		TimeUpdated:  time.Unix(item.TimeUpdated, 0),
		IsCollection: item.FileType == 2,
	}, nil
}

// GetCollectionDetails fetches all items in a collection
func (swc *SteamWorkshopClient) GetCollectionDetails(ctx context.Context, collectionID string) ([]CollectionItem, error) {
	apiURL := "https://api.steampowered.com/ISteamRemoteStorage/GetCollectionDetails/v1/"

	data := url.Values{}
	data.Set("collectioncount", "1")
	data.Set("publishedfileids[0]", collectionID)

	// Retry logic with exponential backoff
	var resp *http.Response
	var err error
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Create a new request for each retry with the form data
		formReq, reqErr := http.NewRequestWithContext(ctx, "POST", apiURL, nil)
		if reqErr != nil {
			return nil, fmt.Errorf("failed to create request: %w", reqErr)
		}
		formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		formReq.PostForm = data
		
		resp, err = swc.httpClient.Do(formReq)
		if err == nil && resp != nil && resp.StatusCode == http.StatusOK {
			break
		}
		if resp != nil && resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if attempt == maxRetries-1 {
				return nil, fmt.Errorf("steam API returned status %d", resp.StatusCode)
			}
		}
		if err != nil && attempt < maxRetries-1 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch collection details after retries: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("no response received from steam API")
	}
	defer resp.Body.Close()

	var result struct {
		Response struct {
			CollectionDetails []struct {
				Children []CollectionItem `json:"children"`
			} `json:"collectiondetails"`
		} `json:"response"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Response.CollectionDetails) == 0 {
		return nil, fmt.Errorf("collection not found")
	}

	return result.Response.CollectionDetails[0].Children, nil
}
