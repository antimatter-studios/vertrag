package refract

import "testing"

const sampleDocument = `{
  "element": "parseResult",
  "content": [
    {
      "element": "category",
      "meta": { "classes": { "element": "array", "content": [{ "element": "string", "content": "api" }] },
                "title": { "element": "string", "content": "Example" } },
      "content": [
        {
          "element": "resource",
          "attributes": { "href": { "element": "string", "content": "/things" } },
          "content": [
            {
              "element": "transition",
              "content": [
                {
                  "element": "httpTransaction",
                  "content": [
                    { "element": "httpRequest",
                      "attributes": { "method": { "element": "string", "content": "GET" },
                                      "headers": { "element": "httpHeaders", "content": [
                                        { "element": "member",
                                          "content": { "key": { "element": "string", "content": "Accept" },
                                                       "value": { "element": "string", "content": "application/json" } } }
                                      ] } },
                      "content": [
                        { "element": "asset",
                          "meta": { "classes": { "element": "array", "content": [{ "element": "string", "content": "messageBody" }] } },
                          "content": "the body" }
                      ] },
                    { "element": "httpResponse",
                      "attributes": { "statusCode": { "element": "string", "content": "200" } } }
                  ]
                }
              ]
            }
          ]
        }
      ]
    }
  ]
}`

func loadSample(t *testing.T) *Element {
	t.Helper()
	root, err := Load([]byte(sampleDocument))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return root
}

func TestLoadDiscriminatesContentShapes(t *testing.T) {
	root := loadSample(t)

	if root.Name != "parseResult" {
		t.Errorf("root name = %q, want parseResult", root.Name)
	}
	if root.Kind != ContentArray {
		t.Errorf("root kind = %v, want array", root.Kind)
	}

	category := root.Children[0]
	if got := category.MetaValue("title"); got != "Example" {
		t.Errorf("title = %q, want Example", got)
	}
	if !category.HasClass("api") {
		t.Error("category should carry the api class")
	}

	request := root.FindRecursive("httpRequest")[0]
	headers := request.Attr("headers")
	member := headers.Children[0]
	if member.Kind != ContentMember {
		t.Fatalf("header kind = %v, want member", member.Kind)
	}
	if key, _ := member.Key.StringValue(); key != "Accept" {
		t.Errorf("header key = %q, want Accept", key)
	}
}

// TestToValueShapes pins the member representation the compiler destructures.
func TestToValueShapes(t *testing.T) {
	root := loadSample(t)
	headers := root.FindRecursive("httpRequest")[0].Attr("headers")

	list, ok := headers.ToValue().([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("headers value = %#v, want a one-element list", headers.ToValue())
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("member value = %#v, want a map", list[0])
	}
	if entry["key"] != "Accept" || entry["value"] != "application/json" {
		t.Errorf("member = %#v, want key/value pair", entry)
	}
}

func TestToValueOmitsAbsentMemberValue(t *testing.T) {
	member := Member("k", nil)
	value := member.ToValue().(map[string]any)
	if _, present := value["value"]; present {
		t.Error("a member with no value should omit the key entirely, not report nil")
	}
}

// TestFindRecursiveChain pins that the ancestry filter constrains order.
func TestFindRecursiveChain(t *testing.T) {
	root := loadSample(t)

	if got := len(root.FindRecursive("resource", "transition")); got != 1 {
		t.Errorf("transitions under a resource = %d, want 1", got)
	}
	// The chain must appear in document order: a resource does not sit inside a
	// transition, so this finds nothing.
	if got := len(root.FindRecursive("transition", "resource")); got != 0 {
		t.Errorf("resources under a transition = %d, want 0", got)
	}
	if got := len(root.FindRecursive("nonexistent")); got != 0 {
		t.Errorf("missing element = %d, want 0", got)
	}
	if got := root.FindRecursive(); got != nil {
		t.Error("FindRecursive with no names should find nothing")
	}
}

// TestParentsDoNotEnterAttributes pins the boundary that keeps a parent search
// from escaping into an unrelated branch.
func TestParentsDoNotEnterAttributes(t *testing.T) {
	root := loadSample(t)
	request := root.FindRecursive("httpRequest")[0]

	if request.FindParent("resource") == nil {
		t.Error("a request should find its enclosing resource")
	}
	if request.FindParentWithClass("api") == nil {
		t.Error("a request should find the api category")
	}

	// A header member lives in an attribute, so it has no structural parents.
	member := request.Attr("headers").Children[0]
	if member.FindParent("resource") != nil {
		t.Error("an element inside an attribute must not reach the resource tree")
	}
}

func TestAccessorsTolerateNil(t *testing.T) {
	var absent *Element

	if absent.String() != "" {
		t.Error("String on a nil element should be empty")
	}
	if absent.Attr("x") != nil {
		t.Error("Attr on a nil element should be nil")
	}
	if absent.MetaValue("title") != "" {
		t.Error("MetaValue on a nil element should be empty")
	}
	if absent.ToValue() != nil {
		t.Error("ToValue on a nil element should be nil")
	}
	if len(absent.Classes()) != 0 {
		t.Error("Classes on a nil element should be empty")
	}
	if len(absent.ContentChildren()) != 0 {
		t.Error("ContentChildren on a nil element should be empty")
	}
}

func TestLoadRejectsMalformedInput(t *testing.T) {
	if _, err := Load([]byte(`{`)); err == nil {
		t.Error("truncated JSON should fail to load")
	}
	if _, err := Load([]byte(`{"element":"x","content":{"neither":1}}`)); err == nil {
		t.Error("object content that is neither an element nor a member should fail")
	}
}

func TestBuilders(t *testing.T) {
	body := Text("asset", "hello")
	body.AddClass("messageBody")

	request := Named("httpRequest", body)
	request.SetAttr("method", String("POST"))

	if request.Child("asset") == nil {
		t.Fatal("Named should attach its children")
	}
	if request.ChildWithClass("asset", "messageBody") == nil {
		t.Error("ChildWithClass should match on the meta class")
	}
	if request.ChildWithClass("asset", "messageBodySchema") != nil {
		t.Error("ChildWithClass should not match a class the element lacks")
	}
	if body.Parent != request {
		t.Error("Append should record the parent, which the compiler walks upwards")
	}
	if request.Attr("method").String() != "POST" {
		t.Error("SetAttr should store the attribute")
	}

	// Attributes deliberately get no parent, so a parent search cannot escape.
	if request.Attr("method").Parent != nil {
		t.Error("an attribute must not be given a structural parent")
	}

	if request.SetAttr("skipped", nil).Attr("skipped") != nil {
		t.Error("setting a nil attribute should be a no-op")
	}

	array := Array(String("a"), Number(1), Bool(true), Null())
	if len(array.Children) != 4 {
		t.Fatalf("Array children = %d, want 4", len(array.Children))
	}
	values := array.ToValue().([]any)
	if values[0] != "a" || values[1] != float64(1) || values[2] != true || values[3] != nil {
		t.Errorf("array value = %#v", values)
	}

	object := Object(Member("k", String("v")))
	if len(object.Children) != 1 {
		t.Error("Object should hold its members")
	}
}

func TestSetSourceMap(t *testing.T) {
	element := New("annotation")
	element.SetSourceMap(3, 5, 3, 9)

	sourceMap := element.Attr("sourceMap")
	if sourceMap == nil {
		t.Fatal("SetSourceMap should store a sourceMap attribute")
	}
	// The shape the compiler reads: array > sourceMap > array > numbers with
	// line and column attributes.
	position := sourceMap.Children[0].Children[0].Children[0]
	if position.Attr("line").ToValue() != float64(3) {
		t.Errorf("line = %v, want 3", position.Attr("line").ToValue())
	}
	if position.Attr("column").ToValue() != float64(5) {
		t.Errorf("column = %v, want 5", position.Attr("column").ToValue())
	}
}
