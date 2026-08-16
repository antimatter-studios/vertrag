package yamldoc

import (
	"bytes"
	"encoding/json"
)

// OrderedMap is a JSON object that remembers the order its keys were written in.
//
// Go's maps do not, and here that matters: both the generated message bodies
// and the JSON Schemas are compared as strings, so a body whose keys come out
// in a different order is a different body. The reference preserves the order
// of the source document throughout, and so must this.
type OrderedMap struct {
	keys   []string
	values map[string]any
}

func NewOrderedMap() *OrderedMap {
	return &OrderedMap{values: map[string]any{}}
}

// Set adds or replaces a key. A replaced key keeps its original position, which
// is what JavaScript object assignment does.
func (m *OrderedMap) Set(key string, value any) {
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func (m *OrderedMap) Get(key string) (any, bool) {
	value, ok := m.values[key]
	return value, ok
}

func (m *OrderedMap) Has(key string) bool {
	_, ok := m.values[key]
	return ok
}

func (m *OrderedMap) Delete(key string) {
	if _, exists := m.values[key]; !exists {
		return
	}
	delete(m.values, key)
	for i, k := range m.keys {
		if k == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

func (m *OrderedMap) Keys() []string { return m.keys }

func (m *OrderedMap) Len() int { return len(m.keys) }

// MarshalJSON writes the object in key order, without HTML escaping, so a value
// containing `<`, `>` or `&` survives as written.
func (m *OrderedMap) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodedKey, err := encodeJSON(key)
		if err != nil {
			return nil, err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')

		encodedValue, err := encodeJSON(m.values[key])
		if err != nil {
			return nil, err
		}
		buf.Write(encodedValue)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func encodeJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
