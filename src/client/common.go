package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rs/zerolog/log"
)

type pcError struct {
	Error string `json:"error"`
}

func (p *PcClient) doActionWithBody(method, url, actionName string, payload any) error {
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		log.Error().Msgf("failed to marshal %s payload: %v", actionName, err)
		return err
	}
	req, err := http.NewRequest(method, url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	var respErr pcError
	if err = json.NewDecoder(resp.Body).Decode(&respErr); err != nil {
		log.Error().Msgf("failed to decode %s response: %v", actionName, err)
		return err
	}
	return errors.New(respErr.Error)
}

func (p *PcClient) doAction(method, url, actionName string) error {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	var respErr pcError
	if err = json.NewDecoder(resp.Body).Decode(&respErr); err != nil {
		log.Error().Msgf("failed to decode %s response: %v", actionName, err)
		return err
	}
	return errors.New(respErr.Error)
}
