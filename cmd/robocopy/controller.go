package main

import (
	"log"
	"sort"
	"time"
)

type JurisdictionDistrictID struct {
	JurisdictionID
	DistrictID
}

type Reporting struct {
	PrecinctsReporting, TotalPrecincts int
	PercentCounted                     float64
}

type SubResult struct {
	Jurisdiction string
	District     string
	JurisdictionID
	DistrictID
	Reporting
	Options []*OptionResult
	orm     map[OptionID]*OptionResult
}

type OptionResult struct {
	Text            string
	Party           string
	Order           int
	TotalVotes      int
	PercentageVotes float64
	FrontRunner     bool
}

type CountyResult struct {
	DistrictID
	Name, Path, Party, OptionText string
}

type Result struct {
	LastUpdate           time.Time
	IsPrimary            bool
	Contest              string
	Party                string
	TotalVotes           int
	VoteFor              int
	PrimaryDescription   string
	SecondaryDescription string
	FullDescription      string

	District     string
	Jurisdiction string
	Reporting

	Options          []*OptionResult
	orm              map[OptionID]*OptionResult
	SubResults       []*SubResult
	SubResultOptions []string
	srm              map[JurisdictionDistrictID]*SubResult
	CountyResults    []CountyResult
}

func makeOptionResultSlice(cid ContestID, m *Metadata, orm map[OptionID]*OptionResult) []*OptionResult {
	metaOptions := m.Contests[cid].Options
	options := make([]*OptionResult, 0, len(metaOptions))
	for _, optM := range metaOptions {
		// If this doesn't exist, we'll end up putting in blanks, which is fine
		optR := orm[optM.ID]
		if optR == nil {
			optR = &OptionResult{}
		}
		optR.Text = optM.Text
		optR.Party = optM.Party(m)
		optR.Order = optM.Order
		options = append(options, optR)
	}
	return options
}

func MapContestResults(m *Metadata, rc *ResultsContainer) map[ContestID]*Result {
	// Get reporting info for later use
	reporting := map[JurisdictionDistrictID]Reporting{}
	for _, report := range rc.DistrictReporting {
		jdid := JurisdictionDistrictID{
			JurisdictionID: report.JurisdictionID,
			DistrictID:     report.DistrictID,
		}
		reporting[jdid] = Reporting{
			PrecinctsReporting: report.PrecinctsReporting,
			TotalPrecincts:     report.TotalPrecincts,
			PercentCounted:     report.PercentCounted,
		}
	}

	// Create entries for all known contests
	contests := make(map[ContestID]*Result, len(m.Contests))
	for cid, contest := range m.Contests {
		dist, jur := contest.DistrictJurisdiction(m)
		jdid := JurisdictionDistrictID{
			JurisdictionID: contest.JurisdictionID(m),
			DistrictID:     contest.District,
		}
		result := &Result{
			IsPrimary:            m.IsPrimary,
			LastUpdate:           rc.LastUpdate,
			Contest:              contest.Name,
			District:             dist,
			Jurisdiction:         jur,
			Party:                contest.Party(m),
			VoteFor:              contest.VoteFor,
			PrimaryDescription:   contest.PrimaryDescription,
			SecondaryDescription: contest.SecondaryDescription,
			FullDescription:      contest.FullDescription,
			Reporting:            reporting[jdid],
			orm:                  make(map[OptionID]*OptionResult),
			srm:                  make(map[JurisdictionDistrictID]*SubResult),
		}
		contests[cid] = result
	}

	// Keep track of repeated contests
	type rawIDs struct {
		OptionID
		JurisdictionID
		DistrictID
	}
	seen := map[rawIDs]*OptionResult{}

	for _, rawResult := range rc.Results {
		cid := m.OptionParents[rawResult.OptionID]
		result, ok := contests[cid]
		if !ok {
			log.Printf("warning: unknown contest: %d", cid)
			continue
		}

		sid := rawIDs{rawResult.OptionID, rawResult.JurisdictionID, rawResult.DistrictID}
		option := seen[sid]
		if option != nil {
			// We've seen this before!?
			// If the total votes don't match, I guess prefer the bigger one?
			if option.TotalVotes < rawResult.TotalVotes {
				option.TotalVotes = rawResult.TotalVotes
			}
			// Just keep going
			continue
		}

		option = &OptionResult{
			TotalVotes: rawResult.TotalVotes,
		}
		seen[sid] = option

		contestM := m.Contests[cid]
		if rawResult.DistrictID == contestM.District &&
			rawResult.JurisdictionID == contestM.JurisdictionID(m) {
			result.orm[rawResult.OptionID] = option
		} else {
			jdid := JurisdictionDistrictID{
				JurisdictionID: rawResult.JurisdictionID,
				DistrictID:     rawResult.DistrictID,
			}
			subres, ok := result.srm[jdid]
			if !ok {
				subDist := rawResult.DistrictID.From(m).Name
				subJur := rawResult.JurisdictionID.From(m).Name
				subres = &SubResult{
					Jurisdiction:   subJur,
					District:       subDist,
					JurisdictionID: rawResult.JurisdictionID,
					DistrictID:     rawResult.DistrictID,
					Reporting:      reporting[jdid],
					orm:            make(map[OptionID]*OptionResult),
				}
				result.SubResults = append(result.SubResults, subres)
				result.srm[jdid] = subres
			}
			subres.orm[rawResult.OptionID] = option
		}
	}

	// set the total votes / percentage / front-runner
	// sort main options by votes, fallback to BoE order
	// sub-results are in BoE order
	// Add CountyResults as needed
	for cid, result := range contests {
		// For some local races, the jurisdiction info seems wrong
		if len(result.orm) == 0 && len(result.SubResults) == 1 {
			result.orm = result.SubResults[0].orm
		}

		result.Options = makeOptionResultSlice(cid, m, result.orm)
		for _, subr := range result.SubResults {
			subr.Options = makeOptionResultSlice(cid, m, subr.orm)
		}

		total := 0
		for _, o := range result.Options {
			total += o.TotalVotes
		}
		if total > 0 {
			result.TotalVotes = total
			tf := float64(total)
			for _, o := range result.Options {
				o.PercentageVotes = float64(o.TotalVotes) / tf * 100
			}
			// Sort to mark the top N front runners
			sort.Slice(result.Options, func(i, j int) bool {
				return result.Options[i].TotalVotes > result.Options[j].TotalVotes
			})
			// Bug: How to deal with ties?
			for i := 0; i < result.VoteFor && i < len(result.Options); i++ {
				result.Options[i].FrontRunner = true
			}
		}
		// Make sub-result column headers by first sorting in BoE order
		sort.Slice(result.Options, func(i, j int) bool {
			return result.Options[i].Order < result.Options[j].Order
		})
		result.SubResultOptions = make([]string, len(result.Options))
		for i, opt := range result.Options {
			result.SubResultOptions[i] = opt.Text
		}
		// Now sort in vote order
		sort.Slice(result.Options, func(i, j int) bool {
			if result.Options[i].TotalVotes == result.Options[j].TotalVotes {
				return result.Options[i].Order < result.Options[j].Order
			}
			return result.Options[i].TotalVotes > result.Options[j].TotalVotes
		})
		sort.Slice(result.SubResults, func(i, j int) bool {
			if result.SubResults[i].District == result.SubResults[j].District {
				return result.SubResults[i].Jurisdiction < result.SubResults[j].Jurisdiction
			}
			return result.SubResults[i].District < result.SubResults[j].District
		})

		for _, subr := range result.SubResults {
			sort.Slice(subr.Options, func(i, j int) bool {
				return subr.Options[i].Order < subr.Options[j].Order
			})
		}

		hasCountyResults := true
		for _, county := range MarylandCounties {
			if _, ok := result.srm[JurisdictionDistrictID{0, county.ID}]; !ok {
				hasCountyResults = false
				break
			}
		}
		if hasCountyResults {
			result.CountyResults = make([]CountyResult, len(MarylandCounties))
			for i, county := range MarylandCounties {
				party, optionText := "", ""
				mostVotes := 0
				subr := result.srm[JurisdictionDistrictID{0, county.ID}]
				for _, opt := range subr.Options {
					if opt.TotalVotes > mostVotes {
						mostVotes = opt.TotalVotes
						party = opt.Party
						optionText = opt.Text
					}
				}
				result.CountyResults[i] = CountyResult{
					DistrictID: county.ID,
					Name:       county.Name,
					Path:       county.Path,
					Party:      party,
					OptionText: optionText,
				}
			}
		}
	}
	return contests
}

func MapDistrictResults(m *Metadata, contests map[ContestID]*Result) map[DistrictID][]*Result {
	districts := make(map[DistrictID][]*Result)
	// Go through all the data and map out the districts
	for cid, result := range contests {
		did := m.Contests[cid].District
		districts[did] = append(districts[did], result)
	}
	// Sort the districts by contest name
	for _, results := range districts {
		sort.Slice(results, func(i, j int) bool {
			return results[i].Contest < results[j].Contest
		})
	}
	return districts
}

func (result Result) TopN(n int) []*OptionResult {
	options := make([]*OptionResult, len(result.Options))
	copy(options, result.Options)
	sort.SliceStable(options, func(i, j int) bool {
		return options[i].TotalVotes > options[j].TotalVotes
	})
	var i int
	for i = 0; i < n && i < len(options); i++ {
	}
	return options[:i]
}

type barkerResult struct {
	ID         ContestID
	Slug, Name string
	Options    []*OptionResult
}

func BarkerResults(contests map[ContestID]*Result) []barkerResult {
	results := []barkerResult{
		{7263, "bs-2018-elections-general-barker-gov", "Governor", nil},
		{7265, "bs-2018-elections-general-barker-ag", "Attorney General", nil},
		{7391, "bs-2018-elections-general-barker-bce", "Baltimore County Executive", nil},
		{7390, "bs-2018-elections-general-barker-aace", "Anne Arundel County Executive", nil},
		{7529, "bs-2018-elections-general-barker-aasa", "Anne Arundel State’s Attorney", nil},
		{7330, "bs-2018-elections-general-barker-del5", "House of Delegates District 5", nil},
	}
	// Hack so I can use this at startup time with an empty map
	if len(contests) == 0 {
		return results
	}
	for i := range results {
		results[i].Options = contests[results[i].ID].TopN(4)
	}
	return results
}
