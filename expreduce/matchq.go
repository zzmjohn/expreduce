package expreduce

import "fmt"

type matchIter interface {
	reset()
	// returns ismatch, pd, isdone
	next() (bool, *PDManager, bool)
}

type dummyMatchIter struct {
	isMatchQ	bool
	pm			*PDManager
	isDone		bool
}

func (this *dummyMatchIter) next() (bool, *PDManager, bool) {
	return this.isMatchQ, this.pm, this.isDone
}

func (this *dummyMatchIter) reset() {}

type multiMatchIter struct {
	matchIters	[]matchIter
	i			int
}

func (this *multiMatchIter) next() (bool, *PDManager, bool) {
	if this.i >= len(this.matchIters) {
		return false, EmptyPD(), true
	}
	matchq, newPd, done := this.matchIters[this.i].next()
	if done {
		this.i++
	}
	done = this.i >= len(this.matchIters)
	return matchq, newPd, done
}

func (this *multiMatchIter) reset() {}

func NewMatchIter(a Ex, b Ex, dm *DefMap, pm *PDManager, cl *CASLogger) (matchIter, bool) {
	// Special case for Except
	except, isExcept := HeadAssertion(b, "Except")
	if isExcept {
		if len(except.Parts) == 2 {
			matchq, _ := IsMatchQ(a, except.Parts[1], dm, EmptyPD(), cl)
			return &dummyMatchIter{!matchq, pm, true}, true
		} else if len(except.Parts) == 3 {
			matchq, _ := IsMatchQ(a, except.Parts[1], dm, EmptyPD(), cl)
			if !matchq {
				matchqb, newPm := IsMatchQ(a, except.Parts[2], dm, pm, cl)
				return &dummyMatchIter{matchqb, newPm, true}, true
			}
			return &dummyMatchIter{false, pm, true}, true
		}
	}
	// Special case for Alternatives
	alts, isAlts := HeadAssertion(b, "Alternatives")
	if isAlts {
		for _, alt := range alts.Parts[1:] {
			// I recently changed the third argument from EmptyPD() to pm
			// because MatchQ[{a, b}, {a_, k | a_}] was returning True, causing
			// problems in some of the boolean patterns. Might need to make
			// similar changes to the other pattern clauses.
			matchq, newPD := IsMatchQ(a, alt, dm, pm, cl)
			if matchq {
				return &dummyMatchIter{matchq, newPD, true}, true
			}
		}
		return &dummyMatchIter{false, pm, true}, true
	}
	// Special case for PatternTest
	patternTest, isPT := HeadAssertion(b, "PatternTest")
	if isPT {
		if len(patternTest.Parts) == 3 {
			matchq, newPD := IsMatchQ(a, patternTest.Parts[1], dm, EmptyPD(), cl)
			if matchq {
				tmpEs := NewEvalStateNoLog(true)
				res := (NewExpression([]Ex{
					patternTest.Parts[2],
					a,
				})).Eval(tmpEs)
				resSymbol, resIsSymbol := res.(*Symbol)
				if resIsSymbol {
					if resSymbol.Name == "True" {
						return &dummyMatchIter{true, newPD, true}, true
					}
				}
			}
			return &dummyMatchIter{false, pm, true}, true
		}
	}
	// Special case for Condition
	condition, isCond := HeadAssertion(b, "Condition")
	if isCond {
		if len(condition.Parts) == 3 {
			mi, cont := NewMatchIter(a, condition.Parts[1], dm, EmptyPD(), cl)
			for cont {
				matchq, newPD, done := mi.next()
				cont = !done
				if matchq {
					tmpEs := NewEvalStateNoLog(true)
					res := condition.Parts[2].DeepCopy()
					res = ReplacePD(res, dm, cl, newPD).Eval(tmpEs)
					resSymbol, resIsSymbol := res.(*Symbol)
					if resIsSymbol {
						if resSymbol.Name == "True" {
							return &dummyMatchIter{true, newPD, true}, true
						}
					}
				}
			}
		}
	}

	// Continue normally
	pm = CopyPD(pm)
	_, aIsFlt := a.(*Flt)
	_, aIsInteger := a.(*Integer)
	_, aIsString := a.(*String)
	_, aIsSymbol := a.(*Symbol)
	aRational, aIsRational := a.(*Rational)
	bRational, bIsRational := b.(*Rational)
	aExpression, aIsExpression := a.(*Expression)
	bExpression, bIsExpression := b.(*Expression)

	// This initial value is just a randomly chosen placeholder
	// TODO, convert headStr to symbol type, have Ex implement getHead() Symbol
	headStr := "Unknown"
	if aIsFlt {
		headStr = "Real"
	} else if aIsInteger {
		headStr = "Integer"
	} else if aIsString {
		headStr = "String"
	} else if aIsExpression {
		headStr = aExpression.Parts[0].String()
	} else if aIsSymbol {
		headStr = "Symbol"
	} else if aIsRational {
		headStr = "Rational"
	}

	if IsBlankTypeOnly(b) {
		ibtc, ibtcNewPDs := IsBlankTypeCapturing(b, a, headStr, pm, cl)
		if ibtc {
			return &dummyMatchIter{true, ibtcNewPDs, true}, true
		}
		return &dummyMatchIter{false, EmptyPD(), true}, true
	}

	// Handle special case for matching Rational[a_Integer, b_Integer]
	if aIsRational && bIsExpression {
		matchq, newPm := isMatchQRational(aRational, bExpression, dm, pm, cl)
		return &dummyMatchIter{matchq, newPm, true}, true
	} else if aIsExpression && bIsRational {
		matchq, newPm := isMatchQRational(bRational, aExpression, dm, pm, cl)
		return &dummyMatchIter{matchq, newPm, true}, true
	}

	if aIsFlt || aIsInteger || aIsString || aIsSymbol || aIsRational {
		return &dummyMatchIter{IsSameQ(a, b, cl), EmptyPD(), true}, true
	} else if !(aIsExpression && bIsExpression) {
		return &dummyMatchIter{false, EmptyPD(), true}, true
	}

	attrs := Attributes{}
	sequenceHead := "Sequence"
	aExpressionSym, aExpressionSymOk := aExpression.Parts[0].(*Symbol)
	bExpressionSym, bExpressionSymOk := bExpression.Parts[0].(*Symbol)
	if aExpressionSymOk && bExpressionSymOk {
		if aExpressionSym.Name == bExpressionSym.Name {
			attrs = aExpressionSym.Attrs(dm)
			sequenceHead = aExpressionSym.Name
		}
	}

	if attrs.Orderless {
		omi, ok := NewOrderlessMatchIter(aExpression.Parts[1:len(aExpression.Parts)], bExpression.Parts[1:len(bExpression.Parts)], attrs.Flat, sequenceHead, dm, pm, cl)
		if !ok {
			return &dummyMatchIter{false, pm, true}, true
		}
		return omi, true
	}

	nomi, ok := NewNonOrderlessMatchIter(aExpression.Parts, bExpression.Parts, []Ex{}, attrs.Flat, sequenceHead, dm, pm, cl)
	if !ok {
		return &dummyMatchIter{false, pm, true}, true
	}
	return nomi, true
}

// TODO: do not export this
func IsMatchQ(a Ex, b Ex, dm *DefMap, pm *PDManager, cl *CASLogger) (bool, *PDManager) {
	mi, cont := NewMatchIter(a, b, dm, pm, cl)
	return GetMatchQ(mi, cont, pm)
}

func isMatchQRational(a *Rational, b *Expression, dm *DefMap, pm *PDManager, cl *CASLogger) (bool, *PDManager) {
	return IsMatchQ(
		NewExpression([]Ex{
			&Symbol{"Rational"},
			&Integer{a.Num},
			&Integer{a.Den},
		}),

		b, dm, pm, cl)
}

func ParseRepeated(e *Expression) (Ex, int, int, bool) {
	min, max := -1, -1
	if len(e.Parts) < 2 {
		return nil, min, max, false
	}
	if len(e.Parts) >= 3 {
		list, isList := HeadAssertion(e.Parts[2], "List")
		if !isList {
			return nil, min, max, false
		}
		if len(list.Parts) != 2 {
			return nil, min, max, false
		}
		i, isInt := list.Parts[1].(*Integer)
		if !isInt {
			return nil, min, max, false
		}
		ival := i.Val.Int64()
		min = int(ival)
		max = min
	}
	return e.Parts[1], min, max, true
}

type orderlessMatchIter struct {
	components		[]Ex
	lhs_components	[]Ex
	pm				*PDManager
	dm				*DefMap
	cl				*CASLogger
	kConstant		int
	contval			int
	perm			[]int
	remainingMatchIter matchIter
	isFlat			bool
	sequenceHead	string
}

func NewOrderlessMatchIter(components []Ex, lhs_components []Ex, isFlat bool, sequenceHead string, dm *DefMap, pm *PDManager, cl *CASLogger) (matchIter, bool) {
	omi := &orderlessMatchIter{}
	omi.components = components
	omi.lhs_components = lhs_components
	// TODO: is copy needed?
	omi.pm = CopyPD(pm)
	omi.cl = cl
	omi.isFlat = isFlat
	omi.sequenceHead = sequenceHead
	omi.dm = dm

	if cl.debugState {
		cl.Infof("Entering OrderlessIsMatchQ(components: %s, lhs_components: %s, pm: %s)", ExArrayToString(components), ExArrayToString(lhs_components), pm)
	}
	nonBS, bs := extractBlankSequences(lhs_components)
	// This is because MatchQ[a + b + c, b + c] == False. We should be careful
	// though because MatchQ[a + b + c, c + __] == True.
	if len(bs) == 0 && len(components) != len(lhs_components) && !isFlat {
		cl.Debugf("len(components) != len(lhs_components). OrderlessMatchQ failed")
		return omi, false
	} else if len(nonBS) > len(components) {
		cl.Debugf("len(nonBS) > len(components). OrderlessMatchQ failed")
		return omi, false
	}

	// After determining that there is a blanksequence, I should go through
	// Each element of the pattern to be matched to see if it even exists within
	// components. I should use MemberQ for this. This can avoid the time-
	// consuming algorithms below

	// These lines are causing MatchQ[a + b, a + b + x___Plus] == True to fail
	for _, mustContain := range lhs_components {
		pat, isPat := HeadAssertion(mustContain, "Pattern")
		_, isRepeated := HeadAssertion(mustContain, "Repeated")
		if isPat {
			_, isRepeated = HeadAssertion(pat.Parts[2], "Repeated")
		}
		if isRepeated {
			continue
		}

		if !MemberQ(components, mustContain, dm, cl) {
			return omi, false
		}
	}

	omi.kConstant = len(components)
	if len(bs) == 1 {
		// This is probably the most common case. It would be rare for us to
		// have multiple BlankSequences in the same LHS. It saves us a lot of
		// time by doing this
		omi.kConstant = len(nonBS)
	}

	// Start iterating through each permutation of LHS expressions
	omi.perm, omi.contval = make([]int, len(components)), 1
	for i := range omi.perm {
		omi.perm[i] = i
	}

	return omi, true
}

// Should a MatchQ call do:
// 1. Modify pm directly <- bad idea. If we attempt a match and it partially
//    matches, we'll have to restore pm from a snapshot
// 2. Return a modified pm <- probably simplest
// 3. Return a pm with fields to add <- would be most efficient, but complicated
//    and could easily be incorrectly used.
// See IsBlankCapturing for a good example of good use.
// Returns if there is a match and the pm that results. This method can be
// called until there is not a match to find all possible matches. It will
// return false from then on.
func (this *orderlessMatchIter) next() (bool, *PDManager, bool) {
	// This block allows us to queue up match iters from the function.
	if this.remainingMatchIter != nil {
		matchq, newPd, done := this.remainingMatchIter.next()
		if done {
			this.remainingMatchIter = nil
		}
		return matchq, newPd, done && this.contval != 1
	}
	for this.contval == 1 {
		this.cl.Debugf("Using perm: %v\n", this.perm)

		// Build a version of components with the correct order. Can I do this
		// more efficiently with a slice notation? Let's copy for now.
		orderedComponents := make([]Ex, len(this.components))
		for oci, ci := range this.perm {
			orderedComponents[oci] = this.components[ci]
		}
		if this.cl.debugState {
			this.cl.Debugf("%s", ExArrayToString(orderedComponents))
		}
		nomi, cont := NewNonOrderlessMatchIter(orderedComponents, this.lhs_components, []Ex{}, this.isFlat, this.sequenceHead, this.dm, this.pm, this.cl)
		// Generate next permutation, if any
		this.contval = nextKPermutation(this.perm, len(this.components), this.kConstant)
		if cont {
			this.remainingMatchIter = nomi
		}
		return false, this.pm, false
	}
	this.cl.Debugf("OrderlessIsMatchQ failed. Context: %s", this.pm)
	return false, this.pm, true
}

func (this *orderlessMatchIter) reset() {}

func GetMatchQ(mi matchIter, cont bool, pm *PDManager) (bool, *PDManager) {
	for cont {
		matchq, newPd, done := mi.next()
		cont = !done
		// TODO: I could probably update my matchiters to only return if they
		// have a match or are done.
		if matchq {
			return true, newPd
		}
	}
	return false, pm
}

func OrderlessIsMatchQ(components []Ex, lhs_components []Ex, isFlat bool, sequenceHead string, dm *DefMap, pm *PDManager, cl *CASLogger) (bool, *PDManager) {
	omi, cont := NewOrderlessMatchIter(components, lhs_components, isFlat, sequenceHead, dm, pm, cl)
	return GetMatchQ(omi, cont, pm)
}

type nonOrderlessMatchIter struct {
	components		[]Ex
	lhs_components	[]Ex
	match_components	[]Ex
	pm				*PDManager
	cl				*CASLogger
	remainingMatchIter matchIter
	isFlat			bool
	sequenceHead	string
	dm				*DefMap
	debugString		string
}

func NewNonOrderlessMatchIter(components []Ex, lhs_components []Ex, match_components []Ex, isFlat bool, sequenceHead string, dm *DefMap, pm *PDManager, cl *CASLogger) (matchIter, bool) {
	nomi := &nonOrderlessMatchIter{}
	nomi.components = components
	nomi.lhs_components = lhs_components
	nomi.match_components = match_components
	nomi.pm = CopyPD(pm)
	nomi.cl = cl
	nomi.isFlat = isFlat
	nomi.sequenceHead = sequenceHead
	nomi.dm = dm

	// This function is now recursive because of the existence of BlankSequence.
	return nomi, true
}

func NewNonOrderlessMatchIterDS(components []Ex, lhs_components []Ex, match_components []Ex, isFlat bool, sequenceHead string, dm *DefMap, pm *PDManager, cl *CASLogger, ds string) (matchIter, bool) {
	nomi := &nonOrderlessMatchIter{}
	nomi.components = components
	nomi.lhs_components = lhs_components
	nomi.match_components = match_components
	nomi.pm = CopyPD(pm)
	nomi.cl = cl
	nomi.isFlat = isFlat
	nomi.sequenceHead = sequenceHead
	nomi.dm = dm
	nomi.debugString = ds

	// This function is now recursive because of the existence of BlankSequence.
	return nomi, true
}

func DefineSequence(pat *Expression, sequence []Ex, isBlank bool, pm *PDManager, isImpliedBs bool, sequenceHead string, dm *DefMap, cl *CASLogger) bool {
	sAsSymbol, sAsSymbolOk := pat.Parts[1].(*Symbol)
	if sAsSymbolOk {
		if isBlank && !isImpliedBs {
			if len(sequence) != 1 {
				fmt.Println("Invalid blank components length!!")
			}
			defined, ispd := pm.patternDefined[sAsSymbol.Name]
			if ispd && !IsSameQ(defined, sequence[0], cl) {
				cl.Debugf("patterns do not match! continuing.")
				return false
			}
			pm.patternDefined[sAsSymbol.Name] = sequence[0]
		} else {
			// otherwise must be sequence type.
			toTryParts := []Ex{&Symbol{"Sequence"}}
			if isImpliedBs {
				toTryParts = []Ex{&Symbol{sequenceHead}}
			}
			toTryParts = append(toTryParts, sequence...)
			target := NewExpression(toTryParts)
			var targetEx Ex = target
			if isImpliedBs && len(target.Parts) == 2 {
				if (target.Parts[0].(*Symbol)).Attrs(dm).OneIdentity {
					targetEx = target.Parts[1]
				}
			}
			defined, ispd := pm.patternDefined[sAsSymbol.Name]
			if ispd && !IsSameQ(defined, targetEx, cl) {
				cl.Debugf("patterns do not match! continuing.")
				return false
			}
			pm.patternDefined[sAsSymbol.Name] = targetEx
		}
	}
	return true
}

// I think for this to work, I must convert all MatchQ functions to iterators in
// the backend. Only the final MatchQ function should try the first match.
// Everything is an iterator that maintains its state. I think its just
// two other functions: NonOrderlessIsMatchQ and IsMatchQ. potentially need to convert consumers of these functions to use the iterator version.
func (this *nonOrderlessMatchIter) next() (bool, *PDManager, bool) {
	// This block allows us to queue up match iters from the function.
	if this.remainingMatchIter != nil {
		matchq, newPd, done := this.remainingMatchIter.next()
		return matchq, newPd, done
	}
	if this.cl.debugState {
		this.cl.Debugf("Entering NonOrderlessIsMatchQ(components: %s, lhs_components: %s, match_components: %s, isFlat: %v, pm: %s)", ExArrayToString(this.components), ExArrayToString(this.lhs_components), ExArrayToString(this.match_components), this.isFlat, this.pm)
	}
	if len(this.lhs_components) == 0 {
		if len(this.components) == 0 {
			this.cl.Debugf("base case: lhs_components is empty. SUCCESSFUL MATCH!!!! Returning.")
		} else {
			this.cl.Debugf("base case: lhs_components is empty. Not successful. Returning.")
		}
		return len(this.components) == 0, this.pm, true
	}
	interesting := false
	if this.lhs_components[0].String() == "a___" && len(this.match_components) == 1 {
		interesting = true
		this.cl.Infof("COOOOOOOOLLLL!!!")
	}

	// Calculate the min and max elements this component can match.
	pat, isPat := HeadAssertion(this.lhs_components[0], "Pattern")
	bns, isBns := HeadAssertion(this.lhs_components[0], "BlankNullSequence")
	bs, isBs := HeadAssertion(this.lhs_components[0], "BlankSequence")
	blank, isBlank := HeadAssertion(this.lhs_components[0], "Blank")
	repeated, isRepeated := HeadAssertion(this.lhs_components[0], "Repeated")
	if isPat {
		bns, isBns = HeadAssertion(pat.Parts[2], "BlankNullSequence")
		bs, isBs = HeadAssertion(pat.Parts[2], "BlankSequence")
		blank, isBlank = HeadAssertion(pat.Parts[2], "Blank")
		repeated, isRepeated = HeadAssertion(pat.Parts[2], "Repeated")
	}
	isImpliedBs := isBlank && this.isFlat

	startI := 1 // also includes implied blanksequence
	if isBns {
		startI = 0
	}
	endI := 1
	var repPat Ex
	if isRepeated {
		newRepPat, repMin, repMax, repOk := ParseRepeated(repeated)
		this.cl.Infof("Encountered repeated")
		repPat = newRepPat
		if (repOk) {
			if repMin != -1 {
				startI = repMin
			}
			if repMax != -1 {
				endI = repMax
			} else {
				// an undefined end can match to the end of the sequence.
				endI = len(this.match_components) + len(this.components)
			}
		} else {
			isRepeated = false
		}
		this.cl.Debugf("test: Determined sequence startI = %v, endI = %v", startI, endI)
	} else if isBns || isBs || isImpliedBs {
		endI = len(this.match_components) + len(this.components)
	}
	this.cl.Debugf("Determined sequence startI = %v, endI = %v", startI, endI)

	if (startI-len(this.match_components)) > len(this.components) {
		// If our current lhs_component requires more components than we have
		// available, return early. TODO: Perhaps also keep track of the min
		// components for the other lhs components and return even earlier
		// if we detect a problem.
		this.cl.Infof("base case: this.components not long enough. Returning.")
		return false, this.pm, true
	}

	form := this.lhs_components[0]
	// These lines effectively strip out the pattern. Might want a refactor
	// later.
	if isBns {
		form = BlankNullSequenceToBlank(bns)
	} else if isImpliedBs {
		form = blank
	} else if isBlank {
		form = blank
	} else if isRepeated {
		form = repPat
	} else if isBs {
		form = BlankSequenceToBlank(bs)
	}

	sequenceHeadAssert := false
	if isImpliedBs {
		blankExpr, isExpr := form.(*Expression)
		if isExpr {
			if len(blankExpr.Parts) >= 2 {
				sym, isSym := blankExpr.Parts[1].(*Symbol)
				if isSym {
					if sym.Name == this.sequenceHead {
						sequenceHeadAssert = true
						form = NewExpression([]Ex{&Symbol{"Blank"}})
					}
				}
				if !sequenceHeadAssert {
					endI = 1
				}
			}
		}
	}
	this.cl.Debugf("Determined sequence startI = %v, endI = %v", startI, endI)
	/*
	if isImpliedBs {
		minReq := 0
		if (&Symbol{this.sequenceHead}).Attrs(this.dm).OneIdentity {
			minReq = 1
		}
		blankExpr, isExpr := form.(*Expression)
		if !isExpr {
			return false, this.pm, true
			//return false, pm
		}
		// if candidate is larger than the minimum and the blank has an assertion
		if len(this.match_components) > minReq && len(blankExpr.Parts) >= 2 {
			sym, isSym := blankExpr.Parts[1].(*Symbol)
			if isSym {
				if sym.Name != this.sequenceHead {
					return false, this.pm, true
					//return false, pm
				}
				//return true, pm
			}
		}
	}*/

	mmi := &multiMatchIter{}
	if startI == 0 && len(this.match_components) == 0 {
		// Try matching nothing at all.
		this.cl.Infof("trying to match nothing at all.")
		updatedPm := CopyPD(this.pm)
		patOk := true
		if isPat {
			patOk = DefineSequence(pat, this.match_components, isBlank, updatedPm, isImpliedBs, this.sequenceHead, this.dm, this.cl)
			this.cl.Infof("is pattern")
		}
		this.cl.Infof("patOk = %v", patOk)
		if patOk {
			nomi, ok := NewNonOrderlessMatchIter(this.components, this.lhs_components[1:], []Ex{}, this.isFlat, this.sequenceHead, this.dm, updatedPm, this.cl)
			if ok {
				this.cl.Infof("appended NonOrderlessMatchIter(%v, %v, []Ex{}, %v) to matchiters", ExArrayToString(this.components), ExArrayToString(this.lhs_components[1:]), updatedPm)
				mmi.matchIters = append(mmi.matchIters, nomi)
			}
		}
	}
	if len(this.match_components) >= endI {
		this.cl.Infof("len(this.match_components) = %v, endI = %v", len(this.match_components), endI)
		this.cl.Infof("base case: match_components too long. Should not happen. Returning.")
		if len(mmi.matchIters) > 0 {
			this.remainingMatchIter = mmi
			return false, this.pm, false
		} else {
			return false, this.pm, true
		}
	}


	/*
		mmi := &multiMatchIter{}
		for j := startI; j <= endI; j++ {
			seqToTry := this.components[0:j]
			remainingComps := this.components[j:]

			seqMatches, newPm := false, EmptyPD()
			if isBns {
				seqMatches, newPm = ExArrayTestRepeatingMatch(seqToTry, BlankNullSequenceToBlank(bns), "", this.dm, this.pm, this.cl)
			} else if isImpliedBs {
				seqMatches, newPm = ExArrayTestRepeatingMatch(seqToTry, blank, this.sequenceHead, this.dm, this.pm, this.cl)
			} else if isRepeated {
				seqMatches, newPm = ExArrayTestRepeatingMatch(seqToTry, repPat, "", this.dm, this.pm, this.cl)
			} else {
				seqMatches, newPm = ExArrayTestRepeatingMatch(seqToTry, BlankSequenceToBlank(bs), "", this.dm, this.pm, this.cl)
			}
			this.cl.Debugf("ExArrayTestRepeatingMatch(%v, %v) = %v", ExArrayToString(seqToTry), this.lhs_components[0], seqMatches)

			if seqMatches {
				tmpPm := CopyPD(newPm)
				if isPat {
					sAsSymbol, sAsSymbolOk := pat.Parts[1].(*Symbol)
					if sAsSymbolOk {
						toTryParts := []Ex{&Symbol{"Sequence"}}
						if isImpliedBs {
							toTryParts = []Ex{&Symbol{this.sequenceHead}}
						}
						toTryParts = append(toTryParts, seqToTry...)
						target := NewExpression(toTryParts)
						var targetEx Ex = target
						if isImpliedBs && len(target.Parts) == 2 {
							if (target.Parts[0].(*Symbol)).Attrs(this.dm).OneIdentity {
								targetEx = target.Parts[1]
							}
						}
						defined, ispd := tmpPm.patternDefined[sAsSymbol.Name]
						if ispd && !IsSameQ(defined, targetEx, this.cl) {
							continue
						}
						tmpPm.patternDefined[sAsSymbol.Name] = targetEx
					}
				}
				nomi, cont := NewNonOrderlessMatchIter(remainingComps, this.lhs_components[1:], []Ex{}, this.isFlat, this.sequenceHead, this.dm, tmpPm, this.cl)
				if cont {
					mmi.matchIters = append(mmi.matchIters, nomi)
				}
			}
		}
		this.remainingMatchIter = mmi
		return false, this.pm, false
	}*/
	this.cl.Debugf("Checking if IsMatchQ(%s, %s). Current context: %v\n", this.components[0], form, this.pm)
	if (interesting) {
		this.cl.Infof("COOOOOL: the above line describes what we're trying.")
	}
	mi, cont := NewMatchIter(this.components[0], form, this.dm, this.pm, this.cl)
	for cont {
		matchq, submatches, done := mi.next()
		cont = !done
		if matchq {
			if (interesting) {
				this.cl.Infof("COOOOOL: actually found a match.")
			}

			if (len(this.match_components)+1 >= startI) && (!sequenceHeadAssert || len(this.match_components) > 0) {
				// We're able to move onto the next lhs_component. Try this.
				updatedPm := CopyPD(this.pm)
				updatedPm.Update(submatches)
				if isPat {
					if !DefineSequence(pat, append(this.match_components, this.components[0]), isBlank, updatedPm, isImpliedBs, this.sequenceHead, this.dm, this.cl) {
						continue
					}
				}
				nomi, ok := NewNonOrderlessMatchIter(this.components[1:], this.lhs_components[1:], []Ex{}, this.isFlat, this.sequenceHead, this.dm, updatedPm, this.cl)
				if ok {
					mmi.matchIters = append(mmi.matchIters, nomi)
				}
			}
			if len(this.match_components)+1 < endI {
				if (interesting) {
					this.cl.Infof("COOOOOL: actually found a match.")
				}
				updatedPm := CopyPD(this.pm)
				updatedPm.Update(submatches)
				// Try continuing with the current sequence.
				new_matched := append(ExArrayDeepCopy(this.match_components), this.components[0])
				nomi, ok := NewNonOrderlessMatchIter(this.components[1:], this.lhs_components, new_matched, this.isFlat, this.sequenceHead, this.dm, updatedPm, this.cl)
				if ok {
					mmi.matchIters = append(mmi.matchIters, nomi)
				}
			}
		}
	}
	this.remainingMatchIter = mmi
	return false, this.pm, false
}

func (this *nonOrderlessMatchIter) reset() {}

func NonOrderlessIsMatchQ(components []Ex, lhs_components []Ex, isFlat bool, sequenceHead string, dm *DefMap, pm *PDManager, cl *CASLogger) (bool, *PDManager) {
	nomi, cont := NewNonOrderlessMatchIter(components, lhs_components, []Ex{}, isFlat, sequenceHead, dm, pm, cl)
	return GetMatchQ(nomi, cont, pm)
}

func extractBlankSequences(components []Ex) (nonBS []Ex, bs []Ex) {
	for _, c := range components {
		pat, isPat := HeadAssertion(c, "Pattern")
		_, isBns := HeadAssertion(c, "BlankNullSequence")
		_, isBs := HeadAssertion(c, "BlankSequence")
		if isPat {
			_, isBns = HeadAssertion(pat.Parts[2], "BlankNullSequence")
			_, isBs = HeadAssertion(pat.Parts[2], "BlankSequence")
		}
		if isBs || isBns {
			bs = append(bs, c)
		} else {
			nonBS = append(nonBS, c)
		}
	}
	return
}

func ExArrayTestRepeatingMatch(array []Ex, blank Ex, sequenceHead string, dm *DefMap, pm *PDManager, cl *CASLogger) (bool, *PDManager) {
	if len(sequenceHead) > 0 {
		// basically, flat
		minReq := 0
		if (&Symbol{sequenceHead}).Attrs(dm).OneIdentity {
			minReq = 1
		}
		blankExpr, isExpr := blank.(*Expression)
		if !isExpr {
			return false, pm
		}
		// if candidate is larger than the minimum and the blank has an assertion
		if len(array) > minReq && len(blankExpr.Parts) >= 2 {
			sym, isSym := blankExpr.Parts[1].(*Symbol)
			if isSym {
				if sym.Name != sequenceHead {
					return false, pm
				}
				return true, pm
			}
		}
	}
	for _, e := range array {
		tmpEs := NewEvalStateNoLog(false)
		// TODO: CHANGEME
		isMatch, newPD := IsMatchQ(e, blank, dm, pm, &tmpEs.CASLogger)
		pm = newPD
		if !isMatch {
			return false, pm
		}
	}
	return true, pm
}
