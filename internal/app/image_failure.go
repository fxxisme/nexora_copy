package app

import (
	"errors"
	"net/url"
	"strings"
)

var errNoImageGenerated = errors.New("upstream returned no image")

type imageTextReplyError struct{ message string }

func (e *imageTextReplyError) Error() string { return e.message }

func imageTextReplyFailure(message string) error {
	message = cleanImageMessage(message)
	if isImageQuotaMessage(message) {
		return errors.New(message)
	}
	return &imageTextReplyError{message: message}
}

func isImageTextReplyFailure(err error) bool {
	var textErr *imageTextReplyError
	return errors.As(err, &textErr)
}

type imageDeliveryError struct{ cause error }

func (e *imageDeliveryError) Error() string { return "image download failed" }
func (e *imageDeliveryError) Unwrap() error { return e.cause }

type imageDownloadTransportError struct{ cause error }

func (e *imageDownloadTransportError) Error() string { return "download failed: connection error" }
func (e *imageDownloadTransportError) Unwrap() error { return e.cause }

func imageDownloadFailure(rawURL string, err error) error {
	if err == nil {
		return nil
	}
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil || !parsed.IsAbs() || strings.EqualFold(parsed.Hostname(), "chatgpt.com") {
		return err
	}
	return &imageDeliveryError{cause: err}
}

func isImageDeliveryFailure(err error) bool {
	var deliveryErr *imageDeliveryError
	return errors.As(err, &deliveryErr)
}
