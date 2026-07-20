package health

import (
	"encoding/json"
	"fmt"
	"io"
)

func writeJSONLine(writer io.Writer, record any) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal structured log: %w", err)
	}
	encoded = append(encoded, '\n')

	written, err := writer.Write(encoded)
	if err != nil {
		return fmt.Errorf("write structured log: %w", err)
	}
	if written != len(encoded) {
		return fmt.Errorf("write structured log: %w: wrote %d of %d bytes", io.ErrShortWrite, written, len(encoded))
	}
	return nil
}
