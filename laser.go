package tusgo

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

func UploadWithRetry(dst *UploadStream, src *os.File) error {

	if _, err := dst.Sync(); err != nil {
		return err
	}
	if _, err := src.Seek(dst.Tell(), io.SeekStart); err != nil {
		return err
	}

	_, err := io.Copy(dst, src)
	attempts := 10
	for err != nil && attempts > 0 {
		if _, ok := err.(net.Error); !ok && !errors.Is(err, ErrChecksumMismatch) {
			return err
		}
		time.Sleep(5 * time.Second)
		attempts--
		_, err = io.Copy(dst, src)
	}
	if attempts == 0 {
		return errors.New("too many attempts to upload the data")
	}
	return nil
}

func CreateUploadFromSize(size int64, cl *Client, partial bool) *Upload {
	u := Upload{}
	//todo 创建成功后 会自动填充 u
	if _, err := cl.CreateUpload(&u, size, partial, nil); err != nil {
		panic(err)
	}
	fmt.Printf("Location: %s\n", u.Location)
	return &u
}

func CreateUploadFromFile(file *os.File, cl *Client, partial bool) *Upload {

	finfo, err := file.Stat()
	if err != nil {
		panic(err)
	}

	u := Upload{}
	if _, err := cl.CreateUpload(&u, finfo.Size(), partial, nil); err != nil {
		panic(err)
	}
	fmt.Printf("Location: %s\n", u.Location)
	return &u
}
func splitFile(inputPath string, part1Path string, part2Path string, splitAt int64) error {
	// 打开原始文件
	file, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}
	totalSize := fileInfo.Size()

	if splitAt <= 0 || splitAt >= totalSize {
		return fmt.Errorf("splitAt 必须在 (0, 文件大小) 之间，当前文件大小：%d", totalSize)
	}

	// 打开 / 创建 第一个分片文件
	part1, err := os.Create(part1Path)
	if err != nil {
		return err
	}
	defer part1.Close()

	// 写入第一部分：从 0 到 splitAt
	_, err = io.CopyN(part1, file, splitAt)
	if err != nil {
		return err
	}

	// 打开 / 创建 第二个分片文件
	part2, err := os.Create(part2Path)
	if err != nil {
		return err
	}
	defer part2.Close()

	// 写入第二部分：从 splitAt 到末尾
	_, err = io.CopyN(part2, file, totalSize-splitAt)
	if err != nil {
		return err
	}

	fmt.Printf("文件已拆分为两部分：\n- %s （大小：%d 字节）\n- %s （大小：%d 字节）\n",
		part1Path, splitAt, part2Path, totalSize-splitAt)
	return nil
}

func invokeSplit() {
	// 示例：将 bigfile.zip 拆成两半
	inputFile := "C:\\Users\\1\\Desktop\\问题容器\\ZF图纸识别\\图纸\\tmp\\tmp.zip" // 你要拆分的原文件
	part1 := "C:\\Users\\1\\Desktop\\问题容器\\ZF图纸识别\\图纸\\tmp\\part1.bin"   // 第一个分片
	part2 := "C:\\Users\\1\\Desktop\\问题容器\\ZF图纸识别\\图纸\\tmp\\part2.bin"   // 第二个分片

	fileInfo, err := os.Stat(inputFile)
	if err != nil {
		panic(err)
	}
	size := fileInfo.Size()

	// 拆分成两半（你也可以自定义 splitAt，比如前 30MB，后 70MB）
	splitAt := size / 2

	err = splitFile(inputFile, part1, part2, splitAt)
	if err != nil {
		panic(err)
	}
}

const (
	chunkSize = 1 * 1024 * 1024 // 每个分片 5MB，可调整成 10MB 等
)

// toValueSlice 辅助函数：[]*tusgo.Upload → []tusgo.Upload
func toValueSlice(in []*Upload) []Upload {
	var out []Upload
	for _, v := range in {
		out = append(out, *v)
	}
	return out
}

var errAbort = errors.New("task aborted")

func retry(attempts int, fn func() error) error {
	for i := 0; i < attempts; i++ {
		err := fn()
		if err == nil {
			return nil
		}
		if errors.Is(err, errAbort) {
			return err
		}

		// 指数退避 + 随机抖动（防雪崩）
		backoff := time.Duration(math.Pow(2, float64(i))) * time.Second
		jitter := time.Duration(rand.Intn(500)) * time.Millisecond
		sleep := backoff + jitter

		fmt.Printf("  重试中... 等待 %.1f 秒 (第 %d/%d 次)\n", sleep.Seconds(), i+1, attempts)
		time.Sleep(sleep)
	}
	return fmt.Errorf("超过最大重试次数")
}

// UploadSingleFileByConcat 方式一：对文件分片 seek 出分片 放到内存，然后上传合并
func UploadSingleFileByConcat(baseUrl string,
	filePath string,
	mapping string, chunkSize int64, retries int, limitRate int,
) error {
	// 1. 初始化 TUS 客户端
	//http://127.0.0.1:8080/files
	baseURL, err := url.Parse(baseUrl)
	if err != nil {
		return err
		//panic(err)
	}
	cl := NewClient(http.DefaultClient, baseURL)
	if _, err = cl.UpdateCapabilities(); err != nil {
		//panic(err)
		return err
	}

	// 2. 打开要上传的大文件
	f, err := os.Open(filePath)
	if err != nil {
		//panic(err)
		return err
	}
	defer f.Close()
	fileInfo, err := f.Stat()
	if err != nil {
		//panic(err)
		return err
	}
	fileSize := fileInfo.Size()
	fmt.Printf("准备上传大文件: %s (总大小: %d 字节)\n", filePath, fileSize)

	// 3. 分片上传控制
	var (
		uploads = make([]*Upload, 0)

		wg      sync.WaitGroup
		offset  int64 = 0
		partNum int
		failed  atomic.Bool
		//uploadMutex sync.Mutex
	)
	rateLimiter := make(chan struct{}, limitRate)

	// 4. 循环分片读取并上传
	//errs := make([]error, 0)
	for offset < fileSize {
		partNum++
		end := offset + chunkSize
		if end > fileSize {
			end = fileSize
		}
		partSize := end - offset

		// 关键修复：使用 SectionReader（支持并发随机读，零竞态！）
		section := io.NewSectionReader(f, offset, partSize)

		wg.Add(1)
		upload := Upload{}
		uploads = append(uploads, &upload)
		go func(part int, r io.Reader, size int64) {
			rateLimiter <- struct{}{}
			defer wg.Done()
			defer func() { <-rateLimiter }()
			if failed.Load() {
				return
			}
			//todo 添加重试的wrapper
			retry(retries, func() error {

				metadata := map[string]string{
					"filename": filepath.Base(filePath),
					//"Upload-Copy-Path": mapping, //这里对于每一个分片不需要 这个 mapping 而是对于最终的 合并的请求 添加mapping！！！
				}

				if _, err := cl.CreateUpload(&upload, size, true, metadata); err != nil {
					//panic(err)
					fmt.Println(err)
					failed.Store(true)
					return err
				}

				stream := NewUploadStream(cl, &upload)
				if _, err := io.Copy(stream, r); err != nil {
					fmt.Println(err)
					failed.Store(true)
					return err
				}

				fmt.Printf("分片 %d 上传完成 (%d bytes)\n", part, size)
				return nil
			})

		}(partNum, section, partSize)

		offset = end

	}
	//todo 到这里 分片任务肯定都已经开始了
	// 9. 等待所有分片上传完成
	wg.Wait()
	if failed.Load() {
		return errors.New("分片上传错误")
	}

	fmt.Printf("所有 %d 个分片上传完成，开始合并...\n", len(uploads))

	// 10. 合并所有分片为一个完整文件
	var final Upload
	metadata := map[string]string{
		"filename":         filepath.Base(filePath),
		"Upload-Copy-Path": mapping, //这里对于每一个分片不需要 这个 mapping 而是对于最终的 合并的请求 添加mapping！！！
	}
	if _, err := cl.ConcatenateUploads(&final, toValueSlice(uploads), metadata); err != nil {
		//panic(err)
		return err
	}
	fmt.Printf("合并成功！最终文件 Location: %s\n", final.Location)
	// 11、合并需要时间的，因此需要阻塞等待合并完成，轮询操作。
	u := Upload{RemoteOffset: OffsetUnknown}
	for {
		if _, err := cl.GetUpload(&u, final.Location); err != nil {
			//panic(err)
			return err
		}
		if u.RemoteOffset != OffsetUnknown && u.RemoteOffset == u.RemoteSize {
			break
		}
		fmt.Println("等待合并完成...")
		time.Sleep(1 * time.Second)
	}

	fmt.Printf("✅ 合并完成！最终文件大小: %d, 已上传: %d\n", u.RemoteSize, u.RemoteOffset)
	return nil
}

func UploadSingleFileSequentially(baseUrl string, filePath string, mapping string) error {
	baseURL, _ := url.Parse(baseUrl)
	cl := NewClient(http.DefaultClient, baseURL)

	// 必须先 UpdateCapabilities，否则某些功能不生效
	if _, err := cl.UpdateCapabilities(); err != nil {
		return err
	}

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	finfo, _ := f.Stat()

	// 关键：必须走创建流程！不能手动造 Location
	var upload Upload
	metadata := map[string]string{
		"filename":         filepath.Base(filePath),
		"Upload-Copy-Path": mapping, //这里注意！！！
	}

	if _, err := cl.CreateUpload(&upload, finfo.Size(), false, metadata); err != nil {
		return fmt.Errorf("create upload failed: %w", err)
	}

	fmt.Printf("Created upload URL: %s\n", upload.Location) // 这行会真正打印出服务器返回的真实 URL

	// 正确创建上传流
	stream := NewUploadStream(cl, &upload)
	//添加重试机制
	if err := UploadWithRetry(stream, f); err != nil {
		return err
	}

	fmt.Printf("上传完成！最终文件位置: %s\n", upload.Location)
	return nil
}
