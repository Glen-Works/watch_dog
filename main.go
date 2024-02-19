package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-redis/redis/v8"
)

var rdb *redis.Client

const (
	errorLockFailed  = "lock failed"
	errorLockTimeout = "Time out"
)

var script = `
		if redis.call('get',KEYS[1]) == ARGV[1] then
			return redis.call('del',KEYS[1])
		else
			return 0
		end
	`

func init() {
	// 連接到Redis服務器
	rdb = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379", // Redis服務器地址
		Password: "1qaz@WSX",       // 可選的：如果設置了密碼，則需要提供密碼
		DB:       0,                // 使用預設的DB
	})
}

func delete(ctx context.Context, key string, value string) {
	// 定義Lua腳本
	luaScript := `
		if redis.call('get',KEYS[1]) == ARGV[1] then
			return redis.call('del',KEYS[1])
		else
			return 0
		end
	`

	// 創建一個Redis Lua腳本
	luaScriptSHA := redis.NewScript(luaScript)

	// 使用EvalSha執行Lua腳本
	result, err := luaScriptSHA.Run(ctx, rdb, []string{key}, value).Result()
	if err != nil {
		fmt.Println(err)
	}

	// 解析結果
	deleted, ok := result.(int64)
	if !ok {
		fmt.Println("Invalid result type")
	}

	if deleted == 1 {
		fmt.Printf("Key '%s' has been deleted.\n", key)
	} else {
		fmt.Printf("Key '%s' was not deleted or has been modified.\n", key)
	}
}

func (l *Lock) TryLock(ctx context.Context) error {

	success, err := l.client.SetNX(ctx, l.key, l.value, l.ttl).Result()
	if err != nil {
		return err
	}

	// 加锁失败
	if !success {
		return errors.New(errorLockFailed)
	}

	// 加锁成功，启动看门狗
	go l.startWatchDog()
	return nil
}

func (l *Lock) Unlock(ctx context.Context) error {
	err := l.script.Run(ctx, l.client, []string{l.key}, l.value).Err()
	// 关闭看门狗
	close(l.watchDog)
	return err
}

func (l *Lock) Lock(ctx context.Context) error {

	// 尝试加锁
	err := l.TryLock(ctx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errors.New(errorLockTimeout)) {
		return err
	}

	// 加锁失败，不断尝试
	ticker := time.NewTicker(l.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// 超时
			return errors.New(errorLockTimeout)
		case <-ticker.C:
			// 重新尝试加锁
			err := l.TryLock(ctx)
			if err == nil {
				return nil
			}
			if !errors.Is(err, errors.New(errorLockTimeout)) {
				return err
			}
		}
	}
}

func (l *Lock) startWatchDog() {
	ticker := time.NewTicker(l.ttl / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			// 延长锁的过期时间
			ctx, cancel := context.WithTimeout(context.Background(), l.ttl/3*2)
			ok, err := l.client.Expire(ctx, l.key, l.ttl).Result()
			cancel()
			// 异常或锁已经不存在则不再续期
			if err != nil || !ok {
				return
			}
		case <-l.watchDog:
			// 已经解锁
			return
		}
	}
}

type Lock struct {
	client   *redis.Client
	script   *redis.Script
	value    string
	key      string
	ttl      time.Duration
	watchDog chan struct{}
}

func newLock(key, value, luaScript string, ttl time.Duration) *Lock {
	return &Lock{
		client:   rdb,
		script:   redis.NewScript(luaScript),
		value:    value,
		key:      key,
		ttl:      ttl,
		watchDog: make(chan struct{}),
	}
}

func main() {
	// 創建一個context，用於設置命令的超時時間
	ctx := context.Background()

	rand.Seed(time.Now().UnixNano())

	// 生成一個0到99的隨機整數
	randomNumber := rand.Intn(100)

	// 定義要使用的key和value
	key := "mykey"
	value := fmt.Sprintf("expected_value_%s", randomNumber)

	lock := newLock(key, value, script, 10*time.Second)

	lock.Lock(ctx)
	// // 使用SETNX命令設置key的值
	// setNXCmd := rdb.SetNX(ctx, key, value, 0)
	// if err := setNXCmd.Err(); err != nil {
	// 	fmt.Println(err)
	// }
	time.Sleep(60 * time.Second)
	lock.Unlock(ctx)
	// delete(ctx, key, value)
}
