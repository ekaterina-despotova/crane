package recommender_test

import (
	"testing"

	analysisv1alph1 "github.com/gocrane/api/analysis/v1alpha1"
	"github.com/gocrane/crane/pkg/recommendation/recommender"
	"github.com/gocrane/crane/pkg/recommendation/recommender/apis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Blank imports trigger init() registration for each recommender.
	_ "github.com/gocrane/crane/pkg/recommendation/recommender/carbonidle"
	_ "github.com/gocrane/crane/pkg/recommendation/recommender/carbonrightsize"
)

func TestCarbonRecommenderRegistration(t *testing.T) {
	emptyRec := apis.Recommender{
		Config: map[string]string{},
	}
	emptyRule := analysisv1alph1.RecommendationRule{}

	t.Run("CarbonIdleResource returns valid instance", func(t *testing.T) {
		instance, err := recommender.GetRecommenderProvider(
			recommender.CarbonIdleResourceRecommender, emptyRec, emptyRule,
		)
		require.NoError(t, err)
		assert.NotNil(t, instance)
		assert.Equal(t, recommender.CarbonIdleResourceRecommender, instance.Name())

		// Verify the instance implements the Recommender interface.
		var _ recommender.Recommender = instance
	})

	t.Run("CarbonRightSizing returns valid instance", func(t *testing.T) {
		instance, err := recommender.GetRecommenderProvider(
			recommender.CarbonRightSizingRecommender, emptyRec, emptyRule,
		)
		require.NoError(t, err)
		assert.NotNil(t, instance)
		assert.Equal(t, recommender.CarbonRightSizingRecommender, instance.Name())

		// Verify the instance implements the Recommender interface.
		var _ recommender.Recommender = instance
	})
}
