package mocks

import (
	"context"
	"reflect"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
	"go.uber.org/mock/gomock"
)

// MockModel is a manual mock of llms.Model interface.
type MockModel struct {
	ctrl     *gomock.Controller
	recorder *MockModelMockRecorder
}

// MockModelMockRecorder is the mock recorder for MockModel.
type MockModelMockRecorder struct {
	mock *MockModel
}

// NewMockModel creates a new mock instance.
func NewMockModel(ctrl *gomock.Controller) *MockModel {
	mock := &MockModel{ctrl: ctrl}
	mock.recorder = &MockModelMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockModel) EXPECT() *MockModelMockRecorder {
	return m.recorder
}

// GenerateContent mocks base method.
func (m *MockModel) GenerateContent(ctx context.Context, messages []schema.MessageContent, options ...llms.CallOption) (*schema.ContentResponse, error) {
	m.ctrl.T.Helper()
	varargs := []any{ctx, messages}
	for _, a := range options {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "GenerateContent", varargs...)
	ret0, _ := ret[0].(*schema.ContentResponse)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Call mocks base method.
func (m *MockModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	m.ctrl.T.Helper()
	varargs := []any{ctx, prompt}
	for _, a := range options {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "Call", varargs...)
	ret0, _ := ret[0].(string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GenerateContent indicates an expected call of GenerateContent.
func (mr *MockModelMockRecorder) GenerateContent(ctx, messages any, options ...any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	varargs := append([]any{ctx, messages}, options...)
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GenerateContent", reflect.TypeOf((*MockModel)(nil).GenerateContent), varargs...)
}

// Call indicates an expected call of Call.
func (mr *MockModelMockRecorder) Call(ctx, prompt any, options ...any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	varargs := append([]any{ctx, prompt}, options...)
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Call", reflect.TypeOf((*MockModel)(nil).Call), varargs...)
}
