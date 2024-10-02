package main
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"github.com/PuerkitoBio/goquery"
	json "github.com/bytedance/sonic"
	log "github.com/sirupsen/logrus"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/tidwall/gjson"
)
var (
	db *leveldb.DB
)
type AnnouncementInfo struct {
	ID          string
	Title       string
	URL         string
	ReleaseTime int64
}
func init() {
	var err error
	db, err = leveldb.OpenFile("./nft_notice.db", nil)
	if err != nil {
		log.WithError(err).Fatalln("init level db error")
	}
}
func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGABRT,
		syscall.SIGKILL,
	)
	defer stop()
	// Start the message loop.
	for {
		select {
		case <-ctx.Done():
			log.Infoln("recv stop signal, exit main proc")
			return
		default:
			h := time.Now().Hour()
			// skip from 0-8
			if h >= 0 && h <= 8 {
				time.Sleep(60 * time.Second)
				break
			}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, err := huoTuMonitor()
				if err != nil {
					log.WithError(err).WithField("p", "HuoTu").Errorln("anouncement monitor err")
				}
			}()
			go func() {
				defer wg.Done()
				_, err := art18Monitor()
				if err != nil {
					log.WithError(err).WithField("p", "art18").Errorln("anouncement monitor err")
				}
			}()
			wg.Wait()
		}
	}
}
func art18Monitor() (announce *AnnouncementInfo, err error) {
	res, err := http.Get(fmt.Sprintf("https://info.18art.art/html/infor/infor.html?v=%d", time.Now().UnixMilli()))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("[%d]%s", res.StatusCode, res.Status)
	}
	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, err
	}
	doc.Find("script").Each(func(i int, scriptSel *goquery.Selection) {
		jsContent := scriptSel.Text()
		if strings.Contains(jsContent, "noticeList") {
			jsContent = jsContent[strings.Index(jsContent, "noticeList")-2:]
			jsContent = strings.TrimSuffix(jsContent, ";")
			gjson.Get(jsContent, "noticeList").ForEach(func(_, cate gjson.Result) bool {
				if cate.Get("classId").Int() == 7 {
					cate.Get("list").ForEach(func(_, notice gjson.Result) bool {
						title := notice.Get("title").String()
						if strings.Contains(title, "活动时间") {
							return true
						}
						id := notice.Get("id").String()
						detailUri := notice.Get("url").String()
						pubTime := notice.Get("time").Int()
						_, err = db.Get([]byte(id), nil)
						if err != nil {
							if errors.Is(err, leveldb.ErrNotFound) {
								// record to db
								content := fmt.Sprintf("# info %s \n ", title)
								content = content + fmt.Sprintf("**PublishTime:** %s \n\n ", time.UnixMilli(pubTime).Format("2006-01-02 15:04:05"))
								content = content + fmt.Sprintf("**[ClickMe](%s):** %s \n\n ", detailUri, detailUri)
								needMaterials, _ := getArt18NoticeDetail(detailUri)
								if needMaterials != nil {
									content = content + "**Materials:** \n\n "
									for material, resellUrl := range needMaterials {
										content = content + fmt.Sprintf("- [%s](%s): %s \n ", material, resellUrl, resellUrl)
									}
								}
								NotifyMD("Art18 Anouncement", content, false, atMobiles)
								announce = &AnnouncementInfo{
									ID:          id,
									Title:       title,
									URL:         detailUri,
									ReleaseTime: pubTime,
								}
								announcementByte, _ := json.Marshal(*announce)
								err = db.Put([]byte(id), announcementByte, nil)
							}
						}
						return false
					})
					return false
				}
				return true
			})
		}
	})
	if announce != nil {
		log.Infoln("new Art18 anouncement:", announce.Title, announce.URL)
	}
	return
}
func getArt18NoticeDetail(uri string) (map[string]string, error) {
	res, err := http.Get(uri)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("[%d]%s", res.StatusCode, res.Status)
	}
	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, err
	}
	neededMaterials := make(map[string]string)
	doc.Find("script").Each(func(i int, scriptSel *goquery.Selection) {
		jsContent := scriptSel.Text()
		if strings.Contains(jsContent, "__injectJson") {
			jsContent = jsContent[strings.Index(jsContent, "__injectJson")+13:]
			jsContent = strings.TrimSuffix(jsContent, ";")
			content := gjson.Get(jsContent, "content").String()
			content, err = url.QueryUnescape(content)
			if err == nil {
				detailDoc, detailErr := goquery.NewDocumentFromReader(bytes.NewReader([]byte(content)))
				if detailErr == nil {
					detailDoc.Find(".album-name_quill").Each(func(_ int, divSel *goquery.Selection) {
						aid, _ := divSel.Parent().Parent().Attr("data-aid")
						if aid != "" {
							neededMaterials[strings.TrimSpace(divSel.Text())] = fmt.Sprintf("https://h5.18art.art/market/resell/?id=%s", aid)
						} else {
							neededMaterials[strings.TrimSpace(divSel.Text())] = ""
						}
					})
				}
			}
			return
		}
	})
	return neededMaterials, nil
}
func huoTuMonitor() (announce *AnnouncementInfo, err error) {
	res, err := http.Get(fmt.Sprintf("https://i.huotu.art/oss/static/notify/n_homepage.html?sub=14&v=%d", time.Now().UnixMilli()))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("[%d]%s", res.StatusCode, res.Status)
	}
	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, err
	}
	doc.Find("script").Each(func(i int, scriptSel *goquery.Selection) {
		jsContent := scriptSel.Text()
		if strings.Contains(jsContent, "wholeList") {
			jsContent = jsContent[strings.Index(jsContent, "=")+1:]
			jsContent = strings.TrimSuffix(jsContent, ";")
			gjson.Get(jsContent, "wholeList").ForEach(func(_, cate gjson.Result) bool {
				if cate.Get("classId").Int() == 14 {
					cate.Get("list").ForEach(func(_, notice gjson.Result) bool {
						title := notice.Get("title").String()
						if strings.Contains(title, "活动时间") {
							return false
						}
						id := notice.Get("id").String()
						detailUri := notice.Get("url").String()
						pubTime := notice.Get("time").Int()
						_, err = db.Get([]byte(id), nil)
						if err != nil {
							if errors.Is(err, leveldb.ErrNotFound) {
								// record to db
								content := fmt.Sprintf("# info %s \n ", title)
								var needMaterials string
								needMaterials, err = getHuoTuNoticeDetail(detailUri)
								if needMaterials != "" {
									content = content + fmt.Sprintf("- **Materials:** %s \n ", needMaterials)
								}
								content = content + fmt.Sprintf("- **PublishTime:** %s \n ", time.UnixMilli(pubTime).Format("2006-01-02 15:04:05"))
								content = content + fmt.Sprintf("- **[ClickMe](%s):** %s \n ", detailUri, detailUri)
								NotifyMD("HuoTu Anouncement", content, false, atMobiles)
								announce = &AnnouncementInfo{
									ID:          id,
									Title:       title,
									URL:         detailUri,
									ReleaseTime: pubTime,
								}
								announcementByte, _ := json.Marshal(*announce)
								err = db.Put([]byte(id), announcementByte, nil)
							}
						}
						return false
					})
					return false
				}
				return true
			})
		}
	})
	if announce != nil {
		log.Infoln("new HuoTu anouncement:", announce.Title, announce.URL)
	}
	return
}
func getHuoTuNoticeDetail(uri string) (string, error) {
	res, err := http.Get(uri)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("[%d]%s", res.StatusCode, res.Status)
	}
	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return "", err
	}
	neededMaterials := ""
	doc.Find("script").Each(func(i int, scriptSel *goquery.Selection) {
		jsContent := scriptSel.Text()
		if strings.Contains(jsContent, "={") {
			jsContent = jsContent[strings.Index(jsContent, "={")+1:]
			jsContent = strings.TrimSuffix(jsContent, ";")
			content := gjson.Get(jsContent, "content").String()
			content, err = url.QueryUnescape(content)
			if err == nil {
				detailDoc, detailErr := goquery.NewDocumentFromReader(bytes.NewReader([]byte(content)))
				if detailErr == nil {
					detailDoc.Find("p").Each(func(_ int, pSel *goquery.Selection) {
						pText := pSel.Text()
						if strings.Contains(pText, "合成材料") {
							neededMaterials = strings.TrimSpace(pText)
						}
					})
				}
			}
			return
		}
	})
	return neededMaterials, nil
}
