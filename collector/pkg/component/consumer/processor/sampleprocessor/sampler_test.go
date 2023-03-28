package sampleprocessor

import (
	"reflect"
	"testing"
)

func TestUnSendIds(t *testing.T) {
	type operations struct {
		datas   []string
		success bool
	}

	tests := []struct {
		args operations
		want []string
	}{
		{
			args: operations{
				datas:   []string{"1", "2", "3"},
				success: false,
			},
			want: []string{"1", "2", "3"},
		},
		{
			args: operations{
				datas:   []string{},
				success: false,
			},
			want: []string{"1", "2", "3"},
		},
		{
			args: operations{
				datas:   []string{"4", "5"},
				success: false,
			},
			want: []string{"1", "2", "3", "4", "5"},
		},
		{
			args: operations{
				datas:   []string{""},
				success: false,
			},
			want: []string{"4", "5"},
		},
		{
			args: operations{
				datas:   []string{"6"},
				success: false,
			},
			want: []string{"4", "5", "6"},
		},
		{
			args: operations{
				datas:   []string{"7"},
				success: false,
			},
			want: []string{"6", "7"},
		},
		{
			args: operations{
				datas:   []string{"8"},
				success: false,
			},
			want: []string{"6", "7", "8"},
		},
		{
			args: operations{
				datas:   []string{"9"},
				success: true,
			},
			want: []string{},
		},
	}
	t.Run("Test UnSendIds", func(t *testing.T) {
		unSendIds := NewUnSendIds(3)
		for _, test := range tests {
			for _, data := range test.args.datas {
				if len(data) > 0 {
					unSendIds.cacheIds(data)
				}
			}
			size := unSendIds.getToSendCount()
			if test.args.success {
				unSendIds.markSent(size)
			} else {
				unSendIds.markUnSent(size)
			}
			if !reflect.DeepEqual(test.want, unSendIds.toSendIds) {
				t.Errorf("expected %+v, got %+v", test.want, unSendIds.toSendIds)
			}
		}
	})
}
