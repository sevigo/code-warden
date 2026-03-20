import { createBrowserRouter } from 'react-router-dom'
import Layout from './components/Layout'
import Dashboard from './routes/index'
import RepoDetail from './routes/repos.$repoId'
import ChatPage from './routes/repos.$repoId.chat'

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
])
