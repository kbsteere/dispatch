const GH_TOKEN = import.meta.env.GH_TOKEN;

export const getStars = async () => {
  const response = await fetch("https://api.github.com/repos/kbsteere/dispatch", {
    method: "GET",
    headers: {
      Authorization: `bearer ${GH_TOKEN}`,
    },
  });

  if (response.status != 200) {
    return { stargazers_count: 8700 };
  }

  const data: { stargazers_count: number } = await response.json();

  return data;
};
