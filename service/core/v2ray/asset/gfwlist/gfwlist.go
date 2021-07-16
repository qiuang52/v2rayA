package gfwlist

import (
	"bytes"
	libSha256 "crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/tidwall/gjson"
	"github.com/v2rayA/v2rayA/common/files"
	"github.com/v2rayA/v2rayA/common/httpClient"
	"github.com/v2rayA/v2rayA/core/v2ray"
	"github.com/v2rayA/v2rayA/core/v2ray/asset"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/extra/gopeed"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type GFWList struct {
	UpdateTime time.Time
	Tag        string
}

var g GFWList
var gMutex sync.Mutex

func GetRemoteGFWListUpdateTime(c *http.Client) (gfwlist GFWList, err error) {
	gMutex.Lock()
	defer gMutex.Unlock()
	if !g.UpdateTime.IsZero() {
		return g, nil
	}
	resp, err := httpClient.HttpGetUsingSpecificClient(c, "https://api.github.com/repos/v2rayA/dist-v2ray-rules-dat/tags")
	if err != nil {
		err = newError("failed to get latest version of GFWList").Base(err)
		return
	}
	b, _ := io.ReadAll(resp.Body)
	defer resp.Body.Close()
	tag := gjson.GetBytes(b, "0.name").Str
	u := gjson.GetBytes(b, "0.commit.url").Str
	if tag == "" || u == "" {
		err = newError("failed to get latest version of GFWList: fail in getting latest tag")
		return
	}
	resp, err = httpClient.HttpGetUsingSpecificClient(c, u)
	if err != nil {
		err = newError("failed to get latest version of GFWList").Base(err)
		return
	}
	b, _ = io.ReadAll(resp.Body)
	t := gjson.GetBytes(b, "commit.committer.date").Time()
	if t.IsZero() {
		err = newError("failed to get latest version of GFWList: fail in getting commit date of latest tag")
		return
	}
	g.Tag = tag
	g.UpdateTime = t
	return g, nil
}
func IsUpdate() (update bool, remoteTime time.Time, err error) {
	gfwlist, err := GetRemoteGFWListUpdateTime(http.DefaultClient)
	if err != nil {
		return
	}
	remoteTime = gfwlist.UpdateTime
	if !asset.IsGFWListExists() {
		//本地文件不存在，那远端必定比本地新
		return false, remoteTime, nil
	}
	//本地文件存在，检查本地版本是否比远端还新
	t, err := asset.GetGFWListModTime()
	if err != nil {
		return
	}
	if !t.Before(remoteTime) {
		//那确实新
		update = true
		return
	}
	return
}

func checkSha256(p string, sha256 string) bool {
	if b, err := os.ReadFile(p); err == nil {
		hash := libSha256.Sum256(b)
		return hex.EncodeToString(hash[:]) == sha256
	} else {
		return false
	}
}

var (
	FailCheckSha = newError("failed to check sum256sum of GFWList file")
	DamagedFile  = newError("damaged GFWList file, update it again please")
)

func plainDownload(path, url string) (err error) {
	resp, err := http.Get(url)
	if err != nil {
		return
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}
func UpdateLocalGFWList() (localGFWListVersionAfterUpdate string, err error) {
	i := 0
	gfwlist, err := GetRemoteGFWListUpdateTime(http.DefaultClient)
	if err != nil {
		return
	}
	assetDir := asset.GetV2rayLocationAsset()
	pathSiteDat := filepath.Join(assetDir, "LoyalsoldierSite.dat")
	pathSiteDatSha256 := filepath.Join(assetDir, "LoyalsoldierSite.dat.sha256sum")
	backupDat := filepath.Join(assetDir, "LoyalsoldierSite.dat.bak")
	var sucBackup bool
	if _, err = os.Stat(pathSiteDat); err == nil {
		//backup
		err = os.Rename(pathSiteDat, backupDat)
		if err != nil {
			err = newError("fail to backup gfwlist file").Base(err)
			return
		}
		sucBackup = true
	}
	u := fmt.Sprintf(`https://cdn.jsdelivr.net/gh/v2rayA/dist-v2ray-rules-dat@%v/geosite.dat`, gfwlist.Tag)
	if err = gopeed.Down(&gopeed.Request{
		Method: "GET",
		URL:    u,
	}, pathSiteDat); err != nil {
		log.Println(err)
		return
	}
	u2 := fmt.Sprintf(`https://cdn.jsdelivr.net/gh/v2rayA/dist-v2ray-rules-dat@%v/geosite.dat.sha256sum`, gfwlist.Tag)
	if err = plainDownload(pathSiteDatSha256, u2); err != nil {
		log.Println(err)
		return
	}
	defer func() {
		if err != nil {
			if sucBackup {
				_ = os.Rename(backupDat, pathSiteDat)
			} else {
				_ = os.Remove(pathSiteDat)
			}
		}
	}()
	var b []byte
	if b, err = os.ReadFile(pathSiteDatSha256); err == nil {
		f := bytes.Fields(b)
		if len(f) < 2 {
			err = FailCheckSha
			return
		}
		if !checkSha256(pathSiteDat, string(f[0])) {
			err = newError(DamagedFile)
			return
		}
	} else {
		err = FailCheckSha
		return
	}
	_ = os.Chtimes(pathSiteDat, gfwlist.UpdateTime, gfwlist.UpdateTime)
	t, err := files.GetFileModTime(pathSiteDat)
	if err == nil {
		localGFWListVersionAfterUpdate = t.Local().Format("2006-01-02")
	}
	_ = os.Remove(pathSiteDatSha256)
	_ = os.Remove(backupDat)
	log.Printf("download[%v]: %v -> SUCCESS\n", i+1, u)
	return
}

func CheckAndUpdateGFWList() (localGFWListVersionAfterUpdate string, err error) {
	update, tRemote, err := IsUpdate()
	if err != nil {
		return
	}
	if update {
		return "", newError(
			"latest version is " + tRemote.Local().Format("2006-01-02") + ". GFWList is up to date",
		)
	}

	/* 更新LoyalsoldierSite.dat */
	localGFWListVersionAfterUpdate, err = UpdateLocalGFWList()
	if err != nil {
		return
	}
	setting := configure.GetSettingNotNil()
	if v2ray.IsV2RayRunning() && //正在使用GFWList模式再重启
		(setting.Transparent == configure.TransparentGfwlist ||
			setting.Transparent == configure.TransparentClose && setting.PacMode == configure.GfwlistMode) {
		err = v2ray.UpdateV2RayConfig(nil)
	}
	return
}
