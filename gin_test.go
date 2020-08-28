package logging

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func hello(c *gin.Context) {
	c.Error(errors.New("test1"))
	c.Error(errors.New("test2"))
	c.JSON(200, "world")
}

func TestGinLogger(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	app := gin.New()
	app.Use(GinLogger())
	app.GET("/hello", hello)
	go app.Run()
	time.Sleep(100 * time.Millisecond)

	_, err := http.Get("http://localhost:8080/hello?k=v")
	if err != nil {
		t.Fatal(err)
	}
}

func TestGinLoggerWithConfig(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	app := gin.New()
	conf := GinLoggerConfig{
		DisableDetails:  false,
		DetailsWithBody: true,
		Formatter:       func(m GinLogMsg) string { return fmt.Sprintln(m.StatusCode, m.RequestURI) },
		TraceIDFunc:     func(c *gin.Context) string { return "xx-xx-xx-xx" },
	}
	app.Use(GinLoggerWithConfig(conf))
	app.POST("/hello", hello)
	go app.Run(":8888")
	time.Sleep(100 * time.Millisecond)

	_, err := http.Post("http://localhost:8888/hello?k=v", "application/json", bytes.NewReader([]byte(`{"k": "v"}`)))
	if err != nil {
		t.Fatal(err)
	}
}
