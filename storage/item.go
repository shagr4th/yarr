package storage

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

type ItemStatus int

const (
	UNREAD  ItemStatus = 0
	READ    ItemStatus = 1
	STARRED ItemStatus = 2
)

var StatusRepresentations = map[ItemStatus]string{
	UNREAD:  "unread",
	READ:    "read",
	STARRED: "starred",
}

var StatusValues = map[string]ItemStatus{
	"unread":  UNREAD,
	"read":    READ,
	"starred": STARRED,
}

func (s ItemStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(StatusRepresentations[s])
}

func (s *ItemStatus) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	*s = StatusValues[str]
	return nil
}

type Item struct {
	Id          int64      `json:"id"`
	GUID        string     `json:"guid"`
	FeedId      int64      `json:"feed_id"`
	Title       string     `json:"title"`
	Link        string     `json:"link"`
	Description string     `json:"description"`
	Content     string     `json:"content"`
	Author      string     `json:"author"`
	Date        *time.Time `json:"date"`
	DateUpdated *time.Time `json:"date_updated"`
	Status      ItemStatus `json:"status"`
	Image       string     `json:"image"`
	Enclosure   string     `json:"enclosure"`
}

type ItemFilter struct {
	FolderID *int64
	FeedID   *int64
	Status   *ItemStatus
	Search   *string

	IDs     *[]int64
	SinceID *int64
	MaxID   *int64
}

type MarkFilter struct {
	FolderID *int64
	FeedID   *int64

	Before *time.Time
}

func (s *Storage) CreateItems(items []Item) bool {
	tx, err := s.db.Begin()
	if err != nil {
		s.log.Print(err)
		return false
	}
	now := time.Now()

	for _, item := range items {
		// WILD: some feeds provide only `item.date_updated` (without `item.date_created`)
		if item.Date == nil {
			item.Date = item.DateUpdated
		}
		// WILD: `item.guid` is not always present
		if item.GUID == "" {
			item.GUID = item.Link
		}
		_, err = tx.Exec(`
			insert into items (
				guid, feed_id, title, link, description,
				content, author,
				date, date_updated, date_arrived,
				status, image, enclosure
			)
			values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			on conflict (feed_id, guid) do update set
			date_updated = ?, date_arrived = ?`,
			item.GUID, item.FeedId, html.UnescapeString(item.Title), item.Link, item.Description,
			item.Content, item.Author,
			item.Date, item.DateUpdated, now,
			UNREAD, item.Image, item.Enclosure,
			// upsert values
			item.DateUpdated, now,
		)
		if err != nil {
			s.log.Print(err)
			if err = tx.Rollback(); err != nil {
				s.log.Print(err)
				return false
			}
			return false
		}
	}
	if err = tx.Commit(); err != nil {
		s.log.Print(err)
		return false
	}
	return true
}

func listQueryPredicate(filter ItemFilter) (string, []interface{}) {
	cond := make([]string, 0)
	args := make([]interface{}, 0)
	if filter.FolderID != nil {
		cond = append(cond, "f.folder_id = ?")
		args = append(args, *filter.FolderID)
	}
	if filter.FeedID != nil {
		cond = append(cond, "i.feed_id = ?")
		args = append(args, *filter.FeedID)
	}
	if filter.Status != nil {
		cond = append(cond, "i.status = ?")
		args = append(args, *filter.Status)
	}
	if filter.Search != nil {
		words := strings.Fields(*filter.Search)
		terms := make([]string, len(words))
		for idx, word := range words {
			terms[idx] = word + "*"
		}

		cond = append(cond, "i.search_rowid in (select rowid from search where search match ?)")
		args = append(args, strings.Join(terms, " "))
	}

	if filter.IDs != nil && len(*filter.IDs) > 0 {
		qmarks := make([]string, len(*filter.IDs))
		idargs := make([]interface{}, len(*filter.IDs))
		for i, id := range *filter.IDs {
			qmarks[i] = "?"
			idargs[i] = id
		}
		cond = append(cond, "i.id in ("+strings.Join(qmarks, ",")+")")
		args = append(args, idargs...)
	}
	if filter.SinceID != nil {
		cond = append(cond, "i.id > ?")
		args = append(args, filter.SinceID)
	}
	if filter.MaxID != nil {
		cond = append(cond, "i.id < ?")
		args = append(args, filter.MaxID)
	}

	predicate := "1"
	if len(cond) > 0 {
		predicate = strings.Join(cond, " and ")
	}

	return predicate, args
}

func (s *Storage) ListItems(filter ItemFilter, offset, limit int, newestFirst bool) []Item {
	predicate, args := listQueryPredicate(filter)
	result := make([]Item, 0, 0)

	order := "date desc"
	if !newestFirst {
		order = "date asc"
	}

	if filter.IDs != nil || filter.SinceID != nil || filter.MaxID != nil {
		order = "i.id asc"
	}

	query := fmt.Sprintf(`
		select
			i.id, i.guid, i.feed_id, i.title, i.link, i.description,
			i.content, i.author, i.date, i.date_updated, i.status, i.image, i.enclosure
		from items i
		join feeds f on f.id = i.feed_id
		where %s
		order by %s
		limit %d offset %d
		`, predicate, order, limit, offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		s.log.Print(err)
		return result
	}
	for rows.Next() {
		var x Item
		err = rows.Scan(
			&x.Id,
			&x.GUID,
			&x.FeedId,
			&x.Title,
			&x.Link,
			&x.Description,
			&x.Content,
			&x.Author,
			&x.Date,
			&x.DateUpdated,
			&x.Status,
			&x.Image,
			&x.Enclosure,
		)
		if err != nil {
			s.log.Print(err)
			return result
		}
		result = append(result, x)
	}
	return result
}

func (s *Storage) CountItems(filter ItemFilter) int64 {
	predicate, args := listQueryPredicate(filter)
	query := fmt.Sprintf(`
		select count(i.id)
		from items i
		join feeds f on f.id = i.feed_id
		where %s`, predicate)
	row := s.db.QueryRow(query, args...)
	if row != nil {
		var result int64
		row.Scan(&result)
		return result
	}
	return 0
}

func (s *Storage) UpdateItemStatus(item_id int64, status ItemStatus) bool {
	_, err := s.db.Exec(`update items set status = ? where id = ?`, status, item_id)
	return err == nil
}

func (s *Storage) MarkItemsRead(filter MarkFilter) bool {
	cond := make([]string, 0)
	args := make([]interface{}, 0)

	if filter.FolderID != nil {
		cond = append(cond, "f.folder_id = ?")
		args = append(args, *filter.FolderID)
	}
	if filter.FeedID != nil {
		cond = append(cond, "i.feed_id = ?")
		args = append(args, *filter.FeedID)
	}
	// TODO: filter.Before
	predicate := "1"
	if len(cond) > 0 {
		predicate = strings.Join(cond, " and ")
	}
	query := fmt.Sprintf(`
		update items set status = %d
		where id in (
			select i.id from items i
			join feeds f on f.id = i.feed_id
			where %s and i.status != %d
		)
		`, READ, predicate, STARRED)
	_, err := s.db.Exec(query, args...)
	if err != nil {
		s.log.Print(err)
	}
	return err == nil
}

type FeedStat struct {
	FeedId       int64 `json:"feed_id"`
	UnreadCount  int64 `json:"unread"`
	StarredCount int64 `json:"starred"`
}

func (s *Storage) FeedStats() []FeedStat {
	result := make([]FeedStat, 0)
	rows, err := s.db.Query(fmt.Sprintf(`
		select
			feed_id,
			sum(case status when %d then 1 else 0 end),
			sum(case status when %d then 1 else 0 end)
		from items
		group by feed_id
	`, UNREAD, STARRED))
	if err != nil {
		s.log.Print(err)
		return result
	}
	for rows.Next() {
		stat := FeedStat{}
		rows.Scan(&stat.FeedId, &stat.UnreadCount, &stat.StarredCount)
		result = append(result, stat)
	}
	return result
}

func HTMLText(s string) string {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(s))
	contents := make([]string, 0)
	for {
		token := tokenizer.Next()
		if token == xhtml.ErrorToken {
			break
		}
		if token == xhtml.TextToken {
			content := strings.TrimSpace(xhtml.UnescapeString(string(tokenizer.Text())))
			if len(content) > 0 {
				contents = append(contents, content)
			}
		}
	}
	return strings.Join(contents, " ")
}

func (s *Storage) SyncSearch() {
	rows, err := s.db.Query(`
		select id, title, content, description
		from items
		where search_rowid is null;
	`)
	if err != nil {
		s.log.Print(err)
		return
	}

	items := make([]Item, 0)
	for rows.Next() {
		var item Item
		rows.Scan(&item.Id, &item.Title, &item.Content, &item.Description)
		items = append(items, item)
	}

	for _, item := range items {
		result, err := s.db.Exec(`
			insert into search (title, description, content) values (?, ?, ?)`,
			item.Title, HTMLText(item.Description), HTMLText(item.Content),
		)
		if err != nil {
			s.log.Print(err)
			return
		}
		if numrows, err := result.RowsAffected(); err == nil && numrows == 1 {
			if rowId, err := result.LastInsertId(); err == nil {
				s.db.Exec(
					`update items set search_rowid = ? where id = ?`,
					rowId, item.Id,
				)
			}
		}
	}
}

func (s *Storage) DeleteOldItems() {
	rows, err := s.db.Query(fmt.Sprintf(`
		select feed_id, count(*) as num_items
		from items
		where status != %d
		group by feed_id
		having num_items > 50
	`, STARRED))

	if err != nil {
		s.log.Print(err)
		return
	}

	feedIds := make([]int64, 0)
	for rows.Next() {
		var id int64
		rows.Scan(&id, nil)
		feedIds = append(feedIds, id)
	}

	for _, feedId := range feedIds {
		result, err := s.db.Exec(`
			delete from items where feed_id = ? and status != ? and date_arrived < ?`,
			feedId,
			STARRED,
			time.Now().Add(-time.Hour*24*90), // 90 days
		)
		if err != nil {
			s.log.Print(err)
			return
		}
		num, err := result.RowsAffected()
		if err != nil {
			s.log.Print(err)
			return
		}
		if num > 0 {
			s.log.Printf("Deleted %d old items (%d)", num, feedId)
		}
	}
}
