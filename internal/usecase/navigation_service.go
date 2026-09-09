package usecase

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"time"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

const navigationTTL = 15 * time.Minute

type NavigationService struct {
	browser *WorkspaceBrowser
	store   domain.NavigationRepository
	clock   func() time.Time
}

func NewNavigationService(browser *WorkspaceBrowser, store domain.NavigationRepository) *NavigationService {
	return &NavigationService{browser: browser, store: store, clock: time.Now}
}

func (s *NavigationService) Start(ctx context.Context, chatID int64) (domain.NavigationState, []domain.DirectoryEntry, error) {
	id, err := randomID()
	if err != nil {
		return domain.NavigationState{}, nil, err
	}
	now := s.clock().UTC()
	state := domain.NavigationState{ID: id, ChatID: chatID, ExpiresAt: now.Add(navigationTTL), CreatedAt: now}
	if err := s.store.SaveNavigation(ctx, state); err != nil {
		return domain.NavigationState{}, nil, err
	}
	entries, err := s.browser.List(ctx, state.CurrentRelativePath)
	if err != nil {
		return domain.NavigationState{}, nil, err
	}
	return state, entries, nil
}

func (s *NavigationService) Enter(ctx context.Context, id string, chatID int64, relativePath string) (domain.NavigationState, []domain.DirectoryEntry, error) {
	state, err := s.authorize(ctx, id, chatID)
	if err != nil {
		return domain.NavigationState{}, nil, err
	}
	project, err := s.browser.Select(ctx, relativePath)
	if err != nil {
		return domain.NavigationState{}, nil, err
	}
	state.CurrentRelativePath = project.RelativePath
	state.ExpiresAt = s.clock().UTC().Add(navigationTTL)
	if err := s.store.SaveNavigation(ctx, state); err != nil {
		return domain.NavigationState{}, nil, err
	}
	entries, err := s.browser.List(ctx, state.CurrentRelativePath)
	if err != nil {
		return domain.NavigationState{}, nil, err
	}
	return state, entries, nil
}

func (s *NavigationService) Back(ctx context.Context, id string, chatID int64) (domain.NavigationState, []domain.DirectoryEntry, error) {
	state, err := s.authorize(ctx, id, chatID)
	if err != nil {
		return domain.NavigationState{}, nil, err
	}
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(state.CurrentRelativePath)))
	if parent == "." {
		parent = ""
	}
	state.CurrentRelativePath = parent
	state.ExpiresAt = s.clock().UTC().Add(navigationTTL)
	if err := s.store.SaveNavigation(ctx, state); err != nil {
		return domain.NavigationState{}, nil, err
	}
	entries, err := s.browser.List(ctx, state.CurrentRelativePath)
	if err != nil {
		return domain.NavigationState{}, nil, err
	}
	return state, entries, nil
}

func (s *NavigationService) Select(ctx context.Context, id string, chatID int64, relativePath string) (domain.Project, error) {
	if _, err := s.authorize(ctx, id, chatID); err != nil {
		return domain.Project{}, err
	}
	project, err := s.browser.Select(ctx, relativePath)
	if err != nil {
		return domain.Project{}, err
	}
	_ = s.store.DeleteNavigation(ctx, id)
	return project, nil
}

func (s *NavigationService) Home(ctx context.Context, id string, chatID int64) (domain.NavigationState, []domain.DirectoryEntry, error) {
	state, err := s.authorize(ctx, id, chatID)
	if err != nil {
		return domain.NavigationState{}, nil, err
	}
	state.CurrentRelativePath = ""
	state.ExpiresAt = s.clock().UTC().Add(navigationTTL)
	if err := s.store.SaveNavigation(ctx, state); err != nil {
		return domain.NavigationState{}, nil, err
	}
	entries, err := s.browser.List(ctx, "")
	if err != nil {
		return domain.NavigationState{}, nil, err
	}
	return state, entries, nil
}

func (s *NavigationService) authorize(ctx context.Context, id string, chatID int64) (domain.NavigationState, error) {
	state, err := s.store.GetNavigation(ctx, id)
	if err != nil {
		return domain.NavigationState{}, err
	}
	if state.ChatID != chatID {
		return domain.NavigationState{}, domain.ErrUnauthorizedNavigation
	}
	return state, nil
}

func randomID() (string, error) {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
