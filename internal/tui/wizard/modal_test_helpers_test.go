package wizard

import (
	"reflect"
	"unsafe"

	"github.com/rivo/tview"
)

func extractModalDone(modal *tview.Modal) func(int, string) {
	field := reflect.ValueOf(modal).Elem().FieldByName("done")
	ptr := unsafe.Pointer(field.UnsafeAddr())
	return *(*func(int, string))(ptr)
}

func extractModalText(modal *tview.Modal) string {
	return reflect.ValueOf(modal).Elem().FieldByName("text").String()
}
