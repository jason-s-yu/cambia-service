// internal/rating/glicko2.go
package rating

import (
	"log"
	"math"

	"github.com/jason-s-yu/cambia/internal/models"
)

const (
	GlickoScale = 173.7178
	DefaultMu   = 1500.0
	DefaultPhi  = 350.0
	Tau         = 0.5
	Epsilon     = 0.000001
)

// Glicko2Rating ...
type Glicko2Rating struct {
	Mu    float64
	Phi   float64
	Sigma float64
}

// NewGlicko2Rating ...
func NewGlicko2Rating(elo, rd, sigma float64) Glicko2Rating {
	return Glicko2Rating{
		Mu:    (elo - DefaultMu) / GlickoScale,
		Phi:   rd / GlickoScale,
		Sigma: sigma,
	}
}

// ToElo ...
func (r Glicko2Rating) ToElo() float64 {
	return r.Mu*GlickoScale + DefaultMu
}

// SingleOrMultiPlayerGlicko2 ...
func SingleOrMultiPlayerGlicko2(allUsers []models.User, scores []float64) []models.User {
	if len(allUsers) != len(scores) {
		log.Printf("Mismatch in user vs. score count. No rating update performed.")
		return allUsers
	}

	// compute average ELO for a single-step approach
	var totalMu float64
	for i := range allUsers {
		totalMu += float64(allUsers[i].Elo1v1)
	}
	avgElo := totalMu / float64(len(allUsers))

	updated := make([]models.User, len(allUsers))
	for i, u := range allUsers {
		// read stored phi_1v1, sigma_1v1
		oldPhi := u.Phi1v1
		oldSigma := u.Sigma1v1
		// transform to Glicko2 space
		r := NewGlicko2Rating(float64(u.Elo1v1), oldPhi, oldSigma)

		opponentElo := (avgElo*float64(len(allUsers)) - float64(u.Elo1v1)) / float64(len(allUsers)-1)
		oppR := NewGlicko2Rating(opponentElo, DefaultPhi, 0.06)

		newR := updateGlicko(r, oppR, scores[i])
		// convert back
		newElo := newR.ToElo()
		u.Elo1v1 = int(math.Round(newElo))

		// update user's phi, sigma in normal Glicko scale
		u.Phi1v1 = newR.Phi * GlickoScale
		u.Sigma1v1 = newR.Sigma

		updated[i] = u
	}
	return updated
}

// updateGlicko ...
func updateGlicko(r, rOpp Glicko2Rating, score float64) Glicko2Rating {
	gVal := g(rOpp.Phi)
	EVal := E(r.Mu, rOpp.Mu, rOpp.Phi)

	v := 1.0 / (gVal * gVal * EVal * (1 - EVal))
	delta := v * gVal * (score - EVal)

	a := math.Log(r.Sigma * r.Sigma)
	A := a
	var B float64
	if delta*delta > r.Phi*r.Phi+v {
		B = math.Log(delta*delta - r.Phi*r.Phi - v)
	} else {
		k := 1.0
		for f(a-k*Tau, r.Phi, v, delta, A) < 0 {
			k++
		}
		B = a - k*Tau
	}

	fA := func(x float64) float64 {
		return f(x, r.Phi, v, delta, A)
	}

	fB := fA(B)
	for i := 0; i < 100; i++ {
		fAVal := fA(A)
		if math.Abs(fAVal) < Epsilon {
			break
		}
		A1 := A
		A = A1 - fAVal*(A1-B)/(fAVal-fB)
		fB = fA(B)
		if math.Abs(A-B) < Epsilon {
			break
		}
	}
	newSigma := math.Exp(A / 2)
	phiStar := math.Sqrt(r.Phi*r.Phi + newSigma*newSigma)
	phiPrime := 1.0 / math.Sqrt(1.0/(phiStar*phiStar)+1.0/v)
	muPrime := r.Mu + phiPrime*phiPrime*gVal*(score-EVal)

	return Glicko2Rating{
		Mu:    muPrime,
		Phi:   phiPrime,
		Sigma: newSigma,
	}
}

func g(phi float64) float64 {
	return 1 / math.Sqrt(1+3*phi*phi/math.Pi/math.Pi)
}

func E(mu, mu2, phi2 float64) float64 {
	return 1.0 / (1.0 + math.Exp(-g(phi2)*(mu-mu2)))
}

// f is the Glicko2 volatility root finding function
func f(x, phi, v, delta, a float64) float64 {
	ex := math.Exp(x)
	num := ex * (delta*delta - phi*phi - v - ex)
	den := 2.0 * (phi*phi + v + ex) * (phi*phi + v + ex)
	return (num / den) - ((x - a) / (Tau * Tau))
}
