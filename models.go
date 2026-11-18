package typesafe

import (
	"context"
	"net/http"
)

// ModelsService exposes the Models API resource. Reach it through [Client.Models].
type ModelsService struct {
	client *Client
}

// List returns the models available to the account, with each model's name, description, and
// release date.
//
//	response, err := client.Models.List(ctx)
//	for _, model := range response.Models {
//		fmt.Println(model.Name, model.ReleaseDate)
//	}
func (s *ModelsService) List(ctx context.Context, opts ...RequestOption) (*ListModelsResponse, error) {
	if s == nil || s.client == nil || s.client.config == nil {
		return nil, &SDKError{Message: "Client is not initialized; construct one with NewClient."}
	}
	options := resolveRequestOptions(opts)
	req, err := s.client.newRequest(http.MethodGet, ModelsPath, nil, options)
	if err != nil {
		return nil, err
	}
	raw, err := execute(ctx, s.client.config, s.client.execClient, req)
	if err != nil {
		return nil, err
	}
	return parseListModelsResponse(raw)
}
