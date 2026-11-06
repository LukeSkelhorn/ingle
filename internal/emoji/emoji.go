// Package emoji holds the emoji pickers for chores and rewards: the curated
// grids, a check that a typed value is a single emoji, and suggestions from
// a title ("Feed the dog" → 🐶) for children who can't read yet.
package emoji

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode"
)

// MaxRunes is the most code points a stored emoji may have (a ZWJ family is
// seven).
const MaxRunes = 16

// Chores is the chore picker: jobs around the house, easy to tell apart.
var Chores = []string{
	"🛏️", "🪥", "🚿", "🛁", "🧼", "👕", "🧦", "👟", "🎒", "📚",
	"📝", "🎹", "🧸", "🧹", "🧽", "🧺", "🍽️", "🥣", "🗑️", "♻️",
	"🌱", "🪴", "🐶", "🐱", "🐟", "🐰", "🐹", "🚗", "🛒", "⚽",
}

// Rewards is the reward picker: treats, outings and things to save up for.
var Rewards = []string{
	"🧱", "🎮", "📚", "🧸", "🎨", "⚽", "🚲", "🛹", "🎧", "📱",
	"🍕", "🍦", "🍫", "🍩", "🍿", "🎬", "🎟️", "🎡", "🏊", "🎳",
	"🌙", "🛌", "📺", "🎁", "💷", "⭐", "👑", "🦄", "🦖", "🚀",
}

// Kind is what an emoji is for, which picks the suggestions.
type Kind int

// Kinds of suggestion.
const (
	Chore Kind = iota
	Reward
)

// Words that suggest an emoji, by kind. Keys are lowercase words or
// phrases, matched as whole words.
var suggestions = map[Kind]map[string]string{
	Chore: {
		"bed": "🛏️", "beds": "🛏️",
		"teeth": "🪥", "tooth": "🪥", "toothbrush": "🪥",
		"shower": "🚿", "bath": "🛁", "wash hands": "🧼", "hands": "🧼",
		"dressed": "👕", "clothes": "👕", "uniform": "👕", "socks": "🧦", "shoes": "👟",
		"bag": "🎒", "school bag": "🎒", "pack": "🎒",
		"read": "📚", "reading": "📚", "book": "📚", "books": "📚",
		"homework": "📝", "spelling": "📝", "spellings": "📝",
		"piano": "🎹", "practice": "🎹", "music": "🎹",
		"toys": "🧸", "tidy": "🧸", "bedroom": "🧸", "room": "🧸",
		"hoover": "🧹", "vacuum": "🧹", "sweep": "🧹", "dust": "🧹",
		"wipe": "🧽", "clean": "🧽", "table": "🧽",
		"laundry": "🧺", "washing": "🧺",
		"dishes": "🍽️", "dishwasher": "🍽️", "plates": "🍽️", "lay the table": "🍽️", "set the table": "🍽️",
		"breakfast": "🥣", "lunch": "🥣",
		"bin": "🗑️", "bins": "🗑️", "rubbish": "🗑️", "trash": "🗑️",
		"recycling": "♻️",
		"plants":    "🌱", "plant": "🌱", "garden": "🌱", "water the plants": "🪴", "weeding": "🌱",
		"dog": "🐶", "dogs": "🐶", "puppy": "🐶", "walk the dog": "🐶",
		"cat": "🐱", "cats": "🐱", "kitten": "🐱",
		"fish": "🐟", "rabbit": "🐰", "bunny": "🐰", "hamster": "🐹", "guinea pig": "🐹",
		"car": "🚗", "shopping": "🛒", "football": "⚽",
	},
	Reward: {
		"lego": "🧱",
		"game": "🎮", "games": "🎮", "gaming": "🎮", "xbox": "🎮", "playstation": "🎮", "switch": "🎮", "minecraft": "🎮",
		"book": "📚", "comic": "📚", "magazine": "📚",
		"toy": "🧸", "teddy": "🧸",
		"craft": "🎨", "paint": "🎨", "painting": "🎨",
		"bike": "🚲", "scooter": "🛹", "skateboard": "🛹",
		"headphones": "🎧",
		"screen":     "📱", "screen time": "📱", "tablet": "📱", "ipad": "📱", "phone": "📱",
		"pizza": "🍕", "ice cream": "🍦", "chocolate": "🍫", "sweets": "🍫", "sweet": "🍫",
		"doughnut": "🍩", "donut": "🍩", "cake": "🍩", "popcorn": "🍿",
		"film": "🎬", "movie": "🎬", "cinema": "🎬",
		"ticket": "🎟️", "trip": "🎟️", "day out": "🎡", "theme park": "🎡", "fair": "🎡",
		"swimming": "🏊", "swim": "🏊", "bowling": "🎳",
		"late": "🌙", "bedtime": "🌙", "stay up": "🌙", "sleepover": "🛌",
		"tv": "📺", "telly": "📺",
		"present": "🎁", "surprise": "🎁", "gift": "🎁",
		"money": "💷", "pocket money": "💷", "cash": "💷",
	},
}

// Suggest returns an emoji for a title, or "" if nothing matches. The
// match that starts earliest in the title wins; at the same place, the
// longer phrase wins ("walk the dog" over "walk").
func Suggest(title string, kind Kind) string {
	text := " " + words(title) + " "
	best, bestAt, bestLen := "", -1, 0
	for key, e := range suggestions[kind] {
		at := strings.Index(text, " "+key+" ")
		if at < 0 {
			continue
		}
		if bestAt < 0 || at < bestAt || at == bestAt && len(key) > bestLen {
			best, bestAt, bestLen = e, at, len(key)
		}
	}
	return best
}

// SuggestionsJSON is the suggestion map for the admin page, which offers a
// suggestion as the title is typed using the same matching as Suggest.
func SuggestionsJSON(kind Kind) string {
	b, err := json.Marshal(suggestions[kind])
	if err != nil {
		return "{}"
	}
	return string(b)
}

// words lowercases s and turns anything but letters and digits into single
// spaces.
func words(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// Valid reports whether s is exactly one emoji: a pictograph (with an
// optional presentation selector, skin tone or tag sequence), several
// joined by zero-width joiners, a flag, or a keycap.
func Valid(s string) bool {
	rs := []rune(s)
	if len(rs) == 0 || len(rs) > MaxRunes {
		return false
	}
	i, ok := element(rs, 0)
	for ok && i < len(rs) && rs[i] == zwj {
		i, ok = element(rs, i+1)
	}
	return ok && i == len(rs)
}

// In reports whether e is in the grid.
func In(grid []string, e string) bool { return slices.Contains(grid, e) }

const (
	zwj      = '‍'
	vs16     = '️'
	vs15     = '︎'
	keycap   = '⃣'
	tagEnd   = '\U000e007f'
	riFirst  = '\U0001f1e6'
	riLast   = '\U0001f1ff'
	skinLow  = '\U0001f3fb'
	skinHigh = '\U0001f3ff'
)

// element reads one emoji (not counting ZWJ joins) starting at rs[i] and
// returns where it ends.
func element(rs []rune, i int) (int, bool) {
	if i >= len(rs) {
		return i, false
	}
	r := rs[i]
	switch {
	case r >= riFirst && r <= riLast: // a flag is two regional indicators
		if i+1 < len(rs) && rs[i+1] >= riFirst && rs[i+1] <= riLast {
			return i + 2, true
		}
		return i, false
	case r >= '0' && r <= '9', r == '#', r == '*': // keycap: 1️⃣
		i++
		if i < len(rs) && rs[i] == vs16 {
			i++
		}
		if i < len(rs) && rs[i] == keycap {
			return i + 1, true
		}
		return i, false
	case !pictograph(r):
		return i, false
	}
	i++
	if i < len(rs) && (rs[i] == vs16 || rs[i] == vs15) {
		i++
	}
	if i < len(rs) && rs[i] >= skinLow && rs[i] <= skinHigh {
		i++
	}
	// Subdivision flags: 🏴 then tag letters, ending in a cancel tag.
	if i < len(rs) && rs[i] >= '\U000e0020' && rs[i] < tagEnd {
		for i < len(rs) && rs[i] >= '\U000e0020' && rs[i] < tagEnd {
			i++
		}
		if i >= len(rs) || rs[i] != tagEnd {
			return i, false
		}
		i++
	}
	return i, true
}

// pictograph reports whether r can start an emoji.
func pictograph(r rune) bool {
	switch {
	case r >= skinLow && r <= skinHigh:
		return false // a skin tone only modifies
	case r >= 0x1f000 && r <= 0x1faff,
		r >= 0x2600 && r <= 0x27bf,
		r >= 0x2300 && r <= 0x23ff,
		r >= 0x2b00 && r <= 0x2bff,
		r >= 0x2190 && r <= 0x21ff:
		return true
	}
	switch r {
	case 0x00a9, 0x00ae, 0x203c, 0x2049, 0x2122, 0x2139, 0x3030, 0x303d, 0x3297, 0x3299:
		return true
	}
	return false
}
