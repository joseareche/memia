package commercial
import ("net/http";"testing")
func TestMountNoConflict(t *testing.T){m:=http.NewServeMux();(&API{}).Mount(func(p string,h http.Handler){m.Handle(p,h)},func(h http.Handler)http.Handler{return h})}
