// Diagnostic read-only DNS/connectivity checks. No account or session data.
package main

import (
 "context"
 "fmt"
 "net"
 "net/http"
 "time"
)
func main(){
 for _,host:=range []string{"long.weixin.qq.com","short.weixin.qq.com"}{
  ctx,cancel:=context.WithTimeout(context.Background(),8*time.Second)
  addresses,err:=net.DefaultResolver.LookupHost(ctx,host);cancel()
  fmt.Printf("dns %s addresses=%v error=%v\n",host,addresses,err)
  connection,err:=net.DialTimeout("tcp",net.JoinHostPort(host,"80"),8*time.Second)
  if connection!=nil{connection.Close()};fmt.Printf("tcp %s error=%v\n",host,err)
 }
 client:=&http.Client{Timeout:8*time.Second,Transport:&http.Transport{Proxy:nil}}
 response,err:=client.Get("http://short.weixin.qq.com/")
 if response!=nil{fmt.Printf("http short status=%d\n",response.StatusCode);response.Body.Close()};fmt.Printf("http short error=%v\n",err)
}
