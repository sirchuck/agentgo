package adapters

import (
	"bytes"
	"encoding/json"
)

// marshalProviderRequestJSON keeps prompt HTML readable in provider request
// bodies instead of expanding safe characters such as < and > into \u escapes.
func marshalProviderRequestJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
