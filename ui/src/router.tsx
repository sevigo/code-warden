import { createBrowserRouter } from 'react-router-dom'
import Layout from './components/Layout'
import Dashboard from './routes/index'
import RepoDetail from './routes/repos.$repoId'
import ChatPage from './routes/repos.$repoId.chat'
import ReviewsPage from './routes/repos.$repoId.reviews'
import ReviewDetailPage from './routes/repos.$repoId.reviews.$prNum'
import JobsPage from './routes/jobs'
import SettingsPage from './routes/settings'

export const router = createBrowserRouter([
  {
    path: '/',
    element: <Layout><Dashboard /></Layout>,
  },
  {
    path: '/repos/:repoId',
    element: <Layout><RepoDetail /></Layout>,
  },
  {
    path: '/repos/:repoId/chat',
    element: <Layout fluid><ChatPage /></Layout>,
  },
  {
    path: '/repos/:repoId/reviews',
    element: <Layout><ReviewsPage /></Layout>,
  },
  {
    path: '/repos/:repoId/reviews/:prNum',
    element: <Layout><ReviewDetailPage /></Layout>,
  },
  {
    path: '/jobs',
    element: <Layout><JobsPage /></Layout>,
  },
  {
    path: '/settings',
    element: <Layout><SettingsPage /></Layout>,
  },
])
