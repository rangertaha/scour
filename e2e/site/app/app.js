// The app. Deliberately small, and deliberately the only place its content
// exists: nothing rendered here is in the HTML that loads it.
//
// Routing is client side through history.pushState, so /app/article/1 is a URL
// the server never sees and a crawler cannot reach by following an href alone.
// That is the point of having it.
(function () {
  var e = React.createElement;

  function Article(props) {
    var a = props.article;
    return e('article', {className: 'story'},
      e('h1', {className: 'headline'}, a.title),
      e('div', {className: 'byline'}, 'By ', e('a', {rel: 'author', href: a.authorUrl}, a.author)),
      e('time', {dateTime: a.published}, a.publishedLabel),
      e('p', {className: 'standfirst'}, a.summary),
      // Links out of the app and back into the static site, so a renderer that
      // runs the script finds somewhere to go next. One is root-relative and
      // one is document-relative, on purpose.
      e('p', null,
        e('a', {href: a.related}, 'Related coverage'), ' ',
        e('a', {href: '../products/'}, 'Products'))
    );
  }

  function App() {
    var state = React.useState(null);
    var articles = state[0], setArticles = state[1];

    React.useEffect(function () {
      fetch('/api/articles.json')
        .then(function (r) { return r.json(); })
        .then(function (d) { setArticles(d.articles); });
    }, []);

    if (!articles) {
      return e('p', {className: 'loading'}, 'Loading…');
    }
    return e('div', {className: 'app'},
      e('h1', null, 'The Gazette, live'),
      articles.map(function (a, i) { return e(Article, {key: i, article: a}); })
    );
  }

  var root = ReactDOM.createRoot(document.getElementById('root'));
  root.render(e(App));
})();
