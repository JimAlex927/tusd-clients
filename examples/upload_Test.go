package main_

func main_() {

	//方式一：通过 concatenate 方式上传 大文件 具体的参数 需要自己设置一些 参数 chunksize这些
	//err := tusgo.UploadSingleFileByConcat("http://127.0.0.1:8080/files",
	//	"C:\\\\Users\\\\1\\\\Downloads\\\\AweSun_16.0.1.24808_x64.exe",
	//	"/$/", 5*1024*1024, 3, 10,
	//)
	//if err != nil {
	//	fmt.Println(err)
	//}

	//方式二 PATCH
	//err := tusgo.UploadSingleFileSequentially("http://127.0.0.1:8080/files",
	//	"C:\\Users\\1\\Downloads\\AweSun_16.0.1.24808_x64.exe", "")
	//if err != nil {
	//	fmt.Println(err)
	//}
}
